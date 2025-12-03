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
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
)

const (
	IDLE_TIMEOUT   = 10 * time.Minute // Increased from 1 min to support warm starts
	MONITOR_PERIOD = 1 * time.Minute
	RUNNER_IMAGE   = "public.ecr.aws/l2r2n3p5/serverless-faas-worker-python:latest"
	NAMESPACE      = "default"
)

var (
	clientset      *kubernetes.Clientset
	redisClient    *redis.Client
	s3Client       *s3.Client
	s3BucketName   string
	awsRegion      string
	lastActivity   sync.Map
	validNameRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

type FunctionRequest struct {
	Name    string `json:"name"`
	Code    string `json:"code"`
	Type    string `json:"type"`
	Request string `json:"request"`
}

type FunctionMetadata struct {
	S3Key     string    `json:"s3_key"`
	PodName   string    `json:"pod_name"`
	Status    string    `json:"status"` // "active", "idle", "terminated"
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func main() {
	// Initialize K8s client
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		panic(err)
	}
	clientset, _ = kubernetes.NewForConfig(k8sConfig)

	// Initialize environment variables
	s3BucketName = os.Getenv("S3_BUCKET_NAME")
	awsRegion = os.Getenv("AWS_REGION")
	redisEndpoint := os.Getenv("REDIS_ENDPOINT")

	if s3BucketName == "" || awsRegion == "" || redisEndpoint == "" {
		log.Fatal("Missing required environment variables: S3_BUCKET_NAME, AWS_REGION, REDIS_ENDPOINT")
	}

	// Initialize AWS SDK
	awsConfig, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(awsRegion))
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}
	s3Client = s3.NewFromConfig(awsConfig)

	// Initialize Redis client
	redisClient = redis.NewClient(&redis.Options{
		Addr:     redisEndpoint,
		Password: "", // ElastiCache Redis doesn't use password by default
		DB:       0,
	})

	// Test Redis connection
	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Println("Connected to Redis successfully")

	// Restore state from Redis
	restoreStateFromRedis(ctx)

	go startIdleMonitor()

	r := gin.Default()

	r.POST("/run", handleDeployAndRun)
	r.POST("/invoke/:name", handleInvoke)
	r.GET("/metrics/:name", handleMetrics)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	log.Println("Serverless Gateway started on :80")
	r.Run(":80")
}

func handleDeployAndRun(c *gin.Context) {
	ctx := context.TODO()
	startTime := time.Now()

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
		if !validNameRegex.MatchString(req.Name) {
			c.JSON(400, gin.H{"error": "Invalid function name. Use lowercase, numbers, hyphens."})
			return
		}
		funcID = req.Name
	} else {
		hash := sha256.Sum256([]byte(req.Code))
		funcID = "func-" + hex.EncodeToString(hash[:])[:10]
	}

	lastActivity.Store(funcID, time.Now())

	// Check Redis for existing function metadata (Warm Start Check)
	metadataKey := fmt.Sprintf("function:%s", funcID)
	isWarmStart := false
	var metadata FunctionMetadata

	metadataJSON, err := redisClient.Get(ctx, metadataKey).Result()
	if err == nil {
		// Function exists in Redis - check if pod is still running
		if err := json.Unmarshal([]byte(metadataJSON), &metadata); err == nil {
			_, err := clientset.CoreV1().Pods(NAMESPACE).Get(ctx, metadata.PodName, metav1.GetOptions{})
			if err == nil {
				// Pod exists and running - WARM START!
				isWarmStart = true
				log.Printf("[%s] Warm Start - Reusing existing pod", funcID)
			}
		}
	}

	// Cold Start - need to create resources
	if !isWarmStart {
		log.Printf("[%s] Cold Start - Creating new resources", funcID)

		// Upload code to S3
		s3Key := fmt.Sprintf("functions/%s.py", funcID)
		_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s3BucketName),
			Key:    aws.String(s3Key),
			Body:   bytes.NewReader([]byte(req.Code)),
		})
		if err != nil {
			log.Printf("[%s] Failed to upload to S3: %v", funcID, err)
			c.JSON(500, gin.H{"error": "Failed to upload code to S3"})
			return
		}
		log.Printf("[%s] Uploaded code to S3: %s", funcID, s3Key)

		// Create K8s resources (Deployment, Service) - NO ConfigMap
		if err := createK8sResources(funcID, req, s3Key); err != nil {
			c.JSON(500, gin.H{"error": "Failed to create resources: " + err.Error()})
			return
		}

		// Wait for pod to be ready
		if !waitForPodReady(funcID) {
			c.JSON(504, gin.H{"error": "Timeout waiting for pod"})
			return
		}

		// Save metadata to Redis
		metadata = FunctionMetadata{
			S3Key:     s3Key,
			PodName:   funcID,
			Status:    "active",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		metadataBytes, _ := json.Marshal(metadata)
		redisClient.Set(ctx, metadataKey, metadataBytes, 0)
		redisClient.SAdd(ctx, "active_functions", funcID)
		log.Printf("[%s] Saved metadata to Redis", funcID)
	} else {
		// Update Redis metadata for warm start
		metadata.UpdatedAt = time.Now()
		metadata.Status = "active"
		metadataBytes, _ := json.Marshal(metadata)
		redisClient.Set(ctx, metadataKey, metadataBytes, 0)
	}

	// Execute function
	target := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", funcID, NAMESPACE)
	proxyRequest(c, target, bodyBytes)

	// Track metrics
	duration := time.Since(startTime).Seconds()
	updateMetrics(ctx, funcID, duration, true, isWarmStart)
}


func handleInvoke(c *gin.Context) {
	ctx := context.TODO()
	startTime := time.Now()
	funcID := c.Param("name")

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "Failed to read body"})
		return
	}

	// Check if function exists in Redis
	metadataKey := fmt.Sprintf("function:%s", funcID)
	_, err = redisClient.Get(ctx, metadataKey).Result()
	if err == redis.Nil {
		c.JSON(404, gin.H{"error": fmt.Sprintf("Function '%s' not found. Please deploy it first via /run", funcID)})
		return
	} else if err != nil {
		c.JSON(500, gin.H{"error": "Redis error"})
		return
	}

	// Check if pod still exists
	_, err = clientset.CoreV1().Services(NAMESPACE).Get(ctx, funcID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		c.JSON(404, gin.H{"error": fmt.Sprintf("Function '%s' pod not found. Please redeploy via /run", funcID)})
		return
	} else if err != nil {
		c.JSON(500, gin.H{"error": "K8s API Error"})
		return
	}

	lastActivity.Store(funcID, time.Now())

	target := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", funcID, NAMESPACE)
	proxyRequest(c, target, bodyBytes)

	// Track metrics
	duration := time.Since(startTime).Seconds()
	updateMetrics(ctx, funcID, duration, true, true) // invoke always uses warm start
}

func createK8sResources(name string, req FunctionRequest, s3Key string) error {
	ctx := context.TODO()

	// NO MORE ConfigMap - code is in S3!

	// 1. Deployment with S3 environment variables
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
						ImagePullPolicy: corev1.PullAlways,
						Ports:           []corev1.ContainerPort{{ContainerPort: 8080}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{"memory": memReq}},
						Env: []corev1.EnvVar{
							{Name: "S3_BUCKET_NAME", Value: s3BucketName},
							{Name: "S3_KEY", Value: s3Key},
							{Name: "AWS_REGION", Value: awsRegion},
						},
						// NO MORE VolumeMounts - code downloaded from S3
					}},
					// NO MORE Volumes
				},
			},
		},
	}

	_, err := clientset.AppsV1().Deployments(NAMESPACE).Create(ctx, deploy, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// 2. Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt(8080)}},
		},
	}
	_, err = clientset.CoreV1().Services(NAMESPACE).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

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

	// Delete K8s resources
	clientset.AppsV1().Deployments(NAMESPACE).Delete(ctx, name, delOpt)
	clientset.CoreV1().Services(NAMESPACE).Delete(ctx, name, delOpt)
	// NO MORE ConfigMap deletion

	// Delete Redis metadata
	metadataKey := fmt.Sprintf("function:%s", name)
	redisClient.Del(ctx, metadataKey)
	redisClient.SRem(ctx, "active_functions", name)

	// Keep metrics for historical purposes (don't delete)

	log.Printf("[%s] Cleaned up resources and Redis metadata", name)
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

func restoreStateFromRedis(ctx context.Context) {
	log.Println("Restoring state from Redis...")

	// Get all active functions
	functions, err := redisClient.SMembers(ctx, "active_functions").Result()
	if err != nil {
		log.Printf("Failed to restore active functions: %v", err)
		return
	}

	for _, funcID := range functions {
		metadataKey := fmt.Sprintf("function:%s", funcID)
		metadataJSON, err := redisClient.Get(ctx, metadataKey).Result()
		if err != nil {
			continue
		}

		var metadata FunctionMetadata
		if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
			continue
		}

		// Verify pod still exists
		_, err = clientset.CoreV1().Pods(NAMESPACE).Get(ctx, metadata.PodName, metav1.GetOptions{})
		if err == nil {
			// Pod exists - restore to memory
			lastActivity.Store(funcID, metadata.UpdatedAt)
			log.Printf("Restored: Function=%s, Pod=%s, S3Key=%s", funcID, metadata.PodName, metadata.S3Key)
		} else {
			// Pod doesn't exist - clean up stale metadata
			log.Printf("Cleaning stale metadata for function=%s (pod not found)", funcID)
			redisClient.Del(ctx, metadataKey)
			redisClient.SRem(ctx, "active_functions", funcID)
		}
	}

	log.Printf("State restoration complete. Active functions: %d", len(functions))
}

func updateMetrics(ctx context.Context, funcID string, duration float64, success bool, isWarmStart bool) {
	metricsKey := fmt.Sprintf("metrics:%s", funcID)

	// Increment execution count
	redisClient.HIncrBy(ctx, metricsKey, "exec_count", 1)

	// Update last run time
	redisClient.HSet(ctx, metricsKey, "last_run_time", time.Now().Format(time.RFC3339))

	// Accumulate total duration
	redisClient.HIncrByFloat(ctx, metricsKey, "total_duration", duration)

	// Track success/failure
	if success {
		redisClient.HIncrBy(ctx, metricsKey, "success_count", 1)
	} else {
		redisClient.HIncrBy(ctx, metricsKey, "error_count", 1)
	}

	// Track warm/cold starts
	if isWarmStart {
		redisClient.HIncrBy(ctx, metricsKey, "warm_start_count", 1)
	} else {
		redisClient.HIncrBy(ctx, metricsKey, "cold_start_count", 1)
	}

	// Set TTL to 7 days
	redisClient.Expire(ctx, metricsKey, 7*24*time.Hour)
}

func handleMetrics(c *gin.Context) {
	ctx := context.TODO()
	funcID := c.Param("name")
	metricsKey := fmt.Sprintf("metrics:%s", funcID)

	metrics, err := redisClient.HGetAll(ctx, metricsKey).Result()
	if err != nil || len(metrics) == 0 {
		c.JSON(404, gin.H{"error": "No metrics found for this function"})
		return
	}

	c.JSON(200, gin.H{
		"function": funcID,
		"metrics":  metrics,
	})
}

func stringPtr(s string) *string { return &s }