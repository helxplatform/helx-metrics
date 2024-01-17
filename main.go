package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	v1 "k8s.io/api/core/v1"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type HelxAppInfo struct {
	PodName  string
	UserName string
}

// Define the Prometheus metric
var reg = prometheus.NewRegistry()
var helxAppGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "helx_app_info", Help: "Information about Helx pods"}, []string{"podname", "username"})
var helxApps map[string]HelxAppInfo
var mutex sync.Mutex

func init() {
	// Register the metric with Prometheus
	reg.MustRegister(helxAppGauge)
	helxApps = make(map[string]HelxAppInfo)
}

func SetUpInformer(clientset *kubernetes.Clientset, namespace string, addFunc func(*v1.Pod), deleteFunc func(*v1.Pod)) {

	factory := informers.NewSharedInformerFactoryWithOptions(clientset, time.Minute, informers.WithNamespace(namespace))
	podInformer := factory.Core().V1().Pods().Informer()

	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if pod, ok := obj.(*v1.Pod); ok {
				addFunc(pod)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if pod, ok := obj.(*v1.Pod); ok {
				deleteFunc(pod)
			}
		},
	})

	go podInformer.Run(context.Background().Done())
}

// HandleAddPod is called when a pod is added
func HandleAddPod(pod *v1.Pod) {
	log.Printf("Pod add event: %s\n", pod.GetName())
	if value, ok := pod.Labels["executor"]; ok && value == "tycho" {
		if username, ok := pod.Labels["username"]; ok {
			appInfo := HelxAppInfo{
				PodName:  pod.GetName(),
				UserName: username,
			}

			mutex.Lock()
			helxApps[pod.GetName()] = appInfo
			helxAppGauge.WithLabelValues(pod.GetName(), username).Set(1)
			mutex.Unlock()

			log.Printf("Added Helx app: %s with username: %s\n", pod.GetName(), username)
		}
	}
}

// HandleDeletePod is called when a pod is deleted
func HandleDeletePod(pod *v1.Pod) {
	log.Printf("Pod deleted: %s\n", pod.GetName())
	mutex.Lock()
	if helxApp, ok := helxApps[pod.GetName()]; ok {
		helxAppGauge.DeleteLabelValues(pod.GetName(), helxApp.UserName) // Delete gauge for this pod
		delete(helxApps, pod.GetName())
		log.Printf("Removed pod: %s\n", pod.GetName())
	}
	mutex.Unlock()
}

// readinessHandler checks the readiness of the service to handle requests.
// In this implementation, it always indicates that the service is ready by
// returning a 200 OK status. In more complex scenarios, this function could
// check internal conditions before determining readiness.
func readinessHandler(w http.ResponseWriter, r *http.Request) {
	// Check conditions to determine if service is ready to handle requests.
	// For simplicity, we're always returning 200 OK in this example.
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Ready"))
}

// livenessHandler checks the health of the service to ensure it's running and
// operational. In this implementation, it always indicates that the service is
// alive by returning a 200 OK status. In more advanced scenarios, this function
// could check internal health metrics before determining liveness.
func livenessHandler(w http.ResponseWriter, r *http.Request) {
	// Check conditions to determine if service is alive and healthy.
	// For simplicity, we're always returning 200 OK in this example.
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Alive"))
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Request received: %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func main() {

	// Set up Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Error creating in-cluster config: %s", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating clientset: %s", err)
	}

	namespaceBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		log.Fatalf("Error reading namespace: %v", err)
	}
	namespace := string(namespaceBytes)

	SetUpInformer(clientset, namespace, HandleAddPod, HandleDeletePod)

	r := mux.NewRouter()
	r.Use(loggingMiddleware)
	r.HandleFunc("/readyz", readinessHandler)
	r.HandleFunc("/healthz", livenessHandler)
	r.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	log.Println("Starting on :9110")
	if err := http.ListenAndServe(":9110", r); err != nil {
		log.Printf("Failed to start server: %v", err)
	}
}
