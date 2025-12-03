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
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
)

const (
	IDLE_TIMEOUT        = 15 * time.Minute
	MONITOR_PERIOD      = 1 * time.Minute
	RUNNER_IMAGE        = "localhost/py-runner:latest"
	NAMESPACE           = "default"
	REDIS_ADDR          = "redis-service:6379"
	
	CONCURRENCY_PER_POD = 5  
	MAX_REPLICAS        = 10 
)

var (
	clientset      *kubernetes.Clientset
	rdb            *redis.Client
	lastActivity   sync.Map 
	
	validNameRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

type FunctionMetadata struct {
	Name string
	Code string
	Type string
	Request string
}

type FunctionRequest struct {
	Name string `json:"name"`
	Code string `json:"code"`
	Type string `json:"type"`
	Request string `json:"request"`
}

func main() {
	config, err := rest.InClusterConfig()
	if err != nil { panic(err) }
	clientset, _ = kubernetes.NewForConfig(config)

	initRedis()
	go startIdleMonitor()
	
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.Use(gin.Recovery())
	
	r.POST("/run", handleDeployAndRun)
	r.POST("/invoke/:name", handleInvoke)

	log.Println("Serverless Gateway (Redis Distributed Counter) started on :80")
	r.Run(":80")
}

func initRedis() {
	rdb = redis.NewClient(&redis.Options{Addr: REDIS_ADDR, DB: 0})
}

func saveFunctionToRedis(req FunctionRequest, funcID string) error {
	meta := FunctionMetadata{Name: funcID, Code: req.Code, Type: req.Type, Request: req.Request}
	data, _ := json.Marshal(meta)
	return rdb.Set(context.Background(), fmt.Sprintf("func:%s", funcID), data, 0).Err()
}

func getFunctionFromRedis(funcID string) (*FunctionMetadata, error) {
	val, err := rdb.Get(context.Background(), fmt.Sprintf("func:%s", funcID)).Result()
	if err != nil { return nil, err }
	var meta FunctionMetadata
	json.Unmarshal([]byte(val), &meta)
	return &meta, nil
}

func incRequestAndScale(funcID string) {
	ctx := context.Background()
	key := fmt.Sprintf("active:%s", funcID)

	currentActive, err := rdb.Incr(ctx, key).Result()
	if err != nil {
		log.Printf("Redis INCR Error: %v", err)
		return
	}
	
	// 요청 처리 예상 시간보다 넉넉하게 잡음 
	rdb.Expire(ctx, key, 1*time.Hour)

	//  스케일업 판단
	neededReplicas := int32(math.Ceil(float64(currentActive) / float64(CONCURRENCY_PER_POD)))
	
	if neededReplicas > 1 {
		go scaleUpDeployment(funcID, neededReplicas)
	}
}

func decRequest(funcID string) {
	ctx := context.Background()
	key := fmt.Sprintf("active:%s", funcID)
	rdb.Decr(ctx, key)
}

func scaleUpDeployment(funcID string, needed int32) {
	if needed > MAX_REPLICAS { needed = MAX_REPLICAS }

	retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deploy, err := clientset.AppsV1().Deployments(NAMESPACE).Get(context.TODO(), funcID, metav1.GetOptions{})
		if err != nil { return err }

		if *deploy.Spec.Replicas < needed {
			log.Printf("[%s] Scaling UP: %d -> %d (Global Active Requests: High)", funcID, *deploy.Spec.Replicas, needed)
			deploy.Spec.Replicas = &needed
			_, err = clientset.AppsV1().Deployments(NAMESPACE).Update(context.TODO(), deploy, metav1.UpdateOptions{})
			return err
		}
		return nil
	})
}

func handleDeployAndRun(c *gin.Context) {
	bodyBytes, _ := io.ReadAll(c.Request.Body)
	var req FunctionRequest
	json.Unmarshal(bodyBytes, &req)

	var funcID string
	if req.Name != "" { funcID = req.Name } else {
		hash := sha256.Sum256([]byte(req.Code))
		funcID = "func-" + hex.EncodeToString(hash[:])[:10]
	}

	saveFunctionToRedis(req, funcID)
	lastActivity.Store(funcID, time.Now())
	ensureK8sResources(c, funcID, req, bodyBytes)
}

func handleInvoke(c *gin.Context) {
	funcID := c.Param("name")
	bodyBytes, _ := io.ReadAll(c.Request.Body)
	
	// 활동 시간 갱신
	lastActivity.Store(funcID, time.Now())
	
	// 서비스 확인
	_, err := clientset.CoreV1().Services(NAMESPACE).Get(context.TODO(), funcID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		meta, err := getFunctionFromRedis(funcID)
		if err != nil { c.JSON(404, gin.H{"error": "Not found"}); return }
		req := FunctionRequest{Name: meta.Name, Code: meta.Code, Type: meta.Type, Request: meta.Request}
		ensureK8sResources(c, funcID, req, bodyBytes)
		return
	}

	target := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", funcID, NAMESPACE)
	proxyRequest(c, funcID, target, bodyBytes)
}

func ensureK8sResources(c *gin.Context, funcID string, req FunctionRequest, bodyBytes []byte) {
	_, err := clientset.CoreV1().Services(NAMESPACE).Get(context.TODO(), funcID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		if err := createK8sResources(funcID, req); err != nil { c.JSON(500, gin.H{"error": err.Error()}); return }
		if !waitForPodReady(funcID) { c.JSON(504, gin.H{"error": "Timeout"}); return }
	}
	target := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", funcID, NAMESPACE)
	proxyRequest(c, funcID, target, bodyBytes)
}


func createK8sResources(name string, req FunctionRequest) error {
	ctx := context.TODO()

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: map[string]string{"user_code.py": req.Code}}
	clientset.CoreV1().ConfigMaps(NAMESPACE).Create(ctx, cm, metav1.CreateOptions{})

	memReq := resource.MustParse("128Mi")
	targetLabel := "small"
	if req.Request == "large-memory" { memReq = resource.MustParse("1Gi"); targetLabel = "large" }

	replicas := int32(1)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas, 
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					RuntimeClassName: stringPtr("gvisor"),
					NodeSelector: map[string]string{"mem_type": targetLabel},
					Containers: []corev1.Container{{
						Name:            "runner",
						Image:           RUNNER_IMAGE,
						ImagePullPolicy: corev1.PullNever,
						Ports:           []corev1.ContainerPort{{ContainerPort: 8080}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{"memory": memReq}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "code-vol", MountPath: "/app/user_code.py", SubPath: "user_code.py"}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt(8080)}},
							InitialDelaySeconds: 1,
							PeriodSeconds: 1,
						},
					}},
					Volumes: []corev1.Volume{{Name: "code-vol", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}},
				},
			},
		},
	}
	_, err := clientset.AppsV1().Deployments(NAMESPACE).Create(ctx, deploy, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) { return err }

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": name}, Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt(8080)}}},
	}
	clientset.CoreV1().Services(NAMESPACE).Create(ctx, svc, metav1.CreateOptions{})
	return nil
}

func startIdleMonitor() {
	ticker := time.NewTicker(MONITOR_PERIOD)
	for range ticker.C {
		now := time.Now()
		lastActivity.Range(func(key, value interface{}) bool {
			funcID := key.(string)
			lastTime := value.(time.Time)
			if now.Sub(lastTime) > IDLE_TIMEOUT {
				log.Printf("[%s] IDLE timeout. Deleting...", funcID)
				deleteK8sResources(funcID)
				lastActivity.Delete(funcID)
				rdb.Del(context.Background(), fmt.Sprintf("active:%s", funcID))
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

func waitForPodReady(funcID string) bool {
    serviceName := fmt.Sprintf("%s.%s.svc.cluster.local:8080", funcID, NAMESPACE)
    timeout := 30 * time.Second
    start := time.Now()
	
	log.Printf("[%s] Waiting for TCP connection...", funcID)

    for {
        if time.Since(start) > timeout {
            return false
        }

        conn, err := net.DialTimeout("tcp", serviceName, 500*time.Millisecond)
        if err == nil {
            conn.Close()
            return true 
        }

        time.Sleep(100 * time.Millisecond)
    }
}

func proxyRequest(c *gin.Context, funcID string, target string, bodyBytes []byte) {
	incRequestAndScale(funcID)
	defer decRequest(funcID)

	remote, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(remote)
	proxy.Transport = &http.Transport{
		DisableKeepAlives: false,
        MaxIdleConnsPerHost: 10,
	}

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = remote.Host
		req.URL.Scheme = remote.Scheme
		req.URL.Host = remote.Host
		req.URL.Path = "/"
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(fmt.Sprintf("Gateway Error: %v", err)))
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func stringPtr(s string) *string { return &s }