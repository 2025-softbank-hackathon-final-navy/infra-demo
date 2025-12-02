package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	IDLE_TIMEOUT   = 1 * time.Minute 
	MONITOR_PERIOD = 1 * time.Minute
	RUNNER_IMAGE   = "localhost/py-runner:latest" 
	NAMESPACE      = "default"
)

var (
	clientset    *kubernetes.Clientset
	lastActivity sync.Map
	// K8s 리소스 이름 유효성 검사 (소문자, 숫자, 하이픈)
	validNameRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

type FunctionRequest struct {
	Name    string `json:"name"` 
	Code    string `json:"code"`
	Type    string `json:"type"`
	Request string `json:"request"`
}

func main() {
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err)
	}
	clientset, _ = kubernetes.NewForConfig(config)

	go startIdleMonitor()

	r := gin.Default()

	r.POST("/run", handleDeployAndRun)
	r.POST("/invoke/:name", handleInvoke)

	log.Println("Serverless Gateway started on :80")
	r.Run(":80")
}

func handleDeployAndRun(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "Failed to read body"})
		return
	}

	var req FunctionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid JSON"})
		return
	}

	var funcID string
	if req.Name != "" {
		// 사용자가 이름을 지정한 경우
		if !validNameRegex.MatchString(req.Name) {
			c.JSON(400, gin.H{"error": "Invalid function name. Use lowercase, numbers, hyphens."})
			return
		}
		funcID = req.Name
	} else {
		// 이름이 없으면 해시 생성, 쓰면 안됨;
		hash := sha256.Sum256([]byte(req.Code))
		funcID = "func-" + hex.EncodeToString(hash[:])[:10]
	}

	// 활동 시간 갱신
	lastActivity.Store(funcID, time.Now())

	// 서비스 존재 확인 -> 없으면 생성 (Cold Start)
	_, err = clientset.CoreV1().Services(NAMESPACE).Get(context.TODO(), funcID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		log.Printf("[%s] Cold Start / Deploying...", funcID)
		if err := createK8sResources(funcID, req); err != nil {
			c.JSON(500, gin.H{"error": "Failed to create resources: " + err.Error()})
			return
		}
		if !waitForPodReady(funcID) {
			c.JSON(504, gin.H{"error": "Timeout waiting for pod"})
			return
		}
	}

	// 실행
	target := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", funcID, NAMESPACE)
	proxyRequest(c, target, bodyBytes)
}


func handleInvoke(c *gin.Context) {
	funcID := c.Param("name") 
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "Failed to read body"})
		return
	}

	// 함수가 존재하는지 확인
	_, err = clientset.CoreV1().Services(NAMESPACE).Get(context.TODO(), funcID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// 함수가 없으면 404 리턴
		c.JSON(404, gin.H{"error": fmt.Sprintf("Function '%s' not found. Please deploy it first via /run", funcID)})
		return
	} else if err != nil {
		c.JSON(500, gin.H{"error": "K8s API Error"})
		return
	}

	// 활동 시간 갱신 
	lastActivity.Store(funcID, time.Now())

	// 실행 요청 포워딩
	target := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", funcID, NAMESPACE)
	proxyRequest(c, target, bodyBytes)
}

func createK8sResources(name string, req FunctionRequest) error {
	ctx := context.TODO()

	// 1. ConfigMap (이미 있으면 업데이트)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Data:       map[string]string{"user_code.py": req.Code},
	}
	
	_, err := clientset.CoreV1().ConfigMaps(NAMESPACE).Create(ctx, cm, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		_, err = clientset.CoreV1().ConfigMaps(NAMESPACE).Update(ctx, cm, metav1.UpdateOptions{})
	}
	if err != nil { return err }

	// 2. Deployment
	memReq := resource.MustParse("128Mi")
	if req.Request == "large-memory" {
		memReq = resource.MustParse("1Gi")
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					RuntimeClassName: stringPtr("gvisor"),
					Containers: []corev1.Container{{
						Name:            "runner",
						Image:           RUNNER_IMAGE,
						ImagePullPolicy: corev1.PullNever, // ECR 환경이면 지우기
						Ports:           []corev1.ContainerPort{{ContainerPort: 8080}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{"memory": memReq}},
						VolumeMounts: []corev1.VolumeMount{{
							Name: "code-vol", MountPath: "/app/user_code.py", SubPath: "user_code.py",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "code-vol",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}},
					}},
				},
			},
		},
	}
	
	_, err = clientset.AppsV1().Deployments(NAMESPACE).Create(ctx, deploy, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) { return err }

	// 3. Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt(8080)}},
		},
	}
	_, err = clientset.CoreV1().Services(NAMESPACE).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) { return err }

	return nil
}

func waitForPodReady(name string) bool {
	log.Printf("[%s] Waiting for pod to run...", name)
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)
		pods, err := clientset.CoreV1().Pods(NAMESPACE).List(context.TODO(), metav1.ListOptions{LabelSelector: "app=" + name})
		if err == nil && len(pods.Items) > 0 {
			if pods.Items[0].Status.Phase == corev1.PodRunning {
				time.Sleep(2 * time.Second) // 서비스 엔드포인트 안정화 대기
				return true
			}
		}
	}
	return false
}

func startIdleMonitor() {
	ticker := time.NewTicker(MONITOR_PERIOD)
	for range ticker.C {
		now := time.Now()
		lastActivity.Range(func(key, value interface{}) bool {
			funcID := key.(string)
			lastTime := value.(time.Time)
			if now.Sub(lastTime) > IDLE_TIMEOUT {
				log.Printf("[%s] IDLE timeout. Cleaning up...", funcID)
				deleteK8sResources(funcID)
				lastActivity.Delete(funcID)
			}
			return true
		})
	}
}

func deleteK8sResources(name string) error {
	ctx := context.TODO()
	delOpt := metav1.DeleteOptions{}
	clientset.AppsV1().Deployments(NAMESPACE).Delete(ctx, name, delOpt)
	clientset.CoreV1().Services(NAMESPACE).Delete(ctx, name, delOpt)
	clientset.CoreV1().ConfigMaps(NAMESPACE).Delete(ctx, name, delOpt)
	return nil
}

func proxyRequest(c *gin.Context, target string, bodyBytes []byte) {
	remote, _ := url.Parse(target)
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.Header = c.Request.Header
			req.Host = remote.Host
			req.URL.Scheme = remote.Scheme
			req.URL.Host = remote.Host
			req.URL.Path = "/"
			
			// Body 리필
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			req.ContentLength = int64(len(bodyBytes))
			req.Header.Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))
		},
		Transport: &http.Transport{DisableKeepAlives: true}, 
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Proxy Error: %v", err)
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(fmt.Sprintf("Gateway Error: %v", err)))
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func stringPtr(s string) *string { return &s }