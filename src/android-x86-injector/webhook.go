package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"log"

	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/kubernetes/pkg/apis/core/v1"
)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()

	// (https://github.com/kubernetes/kubernetes/issues/57982)
	defaulter = runtime.ObjectDefaulter(runtimeScheme)
)

var (
	ignoredNamespaces = []string{
		metav1.NamespaceSystem,
		metav1.NamespacePublic,
	}
)

const (
	kubevirtFlavorKey = "kubevirt.io/flavor"
	kubevirtKey = "kubevirt.io"
	virtLauncherValue = "virt-launcher"
	androidFlavorValue = "android"
)

type WebhookServer struct {
	server *http.Server
}

// Webhook Server parameters
type WhSvrParameters struct {
	port           int    // webhook server port
	certFile       string // path to the x509 certificate for https
	keyFile        string // path to the x509 private key matching `CertFile`
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func init() {
	_ = corev1.AddToScheme(runtimeScheme)
	_ = admissionregistrationv1beta1.AddToScheme(runtimeScheme)
	// defaulting with webhooks:
	// https://github.com/kubernetes/kubernetes/issues/57982
	_ = v1.AddToScheme(runtimeScheme)
}

func mutationRequired(ignoredList []string, metadata *metav1.ObjectMeta) bool {
	// skip special kubernetes system namespaces
	for _, namespace := range ignoredList {
		if metadata.Namespace == namespace {
			log.Printf("Skip mutation for %v because it is in special namespace '%v'", metadata.Name, metadata.Namespace)
			return false
		}
	}

	labels := metadata.GetLabels()
	if labels == nil {
		log.Printf("Skip mutation for %v because it doesn't have any labels", metadata.Name)
		return false
	}

	if flavor, hasLabel := labels[kubevirtFlavorKey]; hasLabel {
		if flavor != androidFlavorValue {
			log.Printf("Skip mutation for %v because it has the flavor '%v' label instead of the expected flavor '%v'", metadata.Name, flavor, androidFlavorValue)
			return false
		}
	} else {
		log.Printf("Skip mutation for %v because it doesn't have the '%v' label", metadata.Name, kubevirtFlavorKey)
		return false
	}

	if flavor, hasLabel := labels[kubevirtKey]; hasLabel {
		if flavor != virtLauncherValue {
			log.Printf("Skip mutation for %v because it has the flavor '%v' label instead of the expected value '%v'", metadata.Name, flavor, virtLauncherValue)
			return false
		}
	} else {
		log.Printf("Skip mutation for %v because it doesn't have the '%v' label", metadata.Name, kubevirtKey)
		return false
	}

	log.Printf("Mutation policy for %v/%v: required", metadata.Namespace, metadata.Name)
	return true
}

func patchPod(pod corev1.Pod) ([]byte, error) {
	var patch []patchOperation

	computeIndex := -1

	for i := 0; i < len(pod.Spec.Containers); i++ {
		container := pod.Spec.Containers[i]

		log.Printf("Found a '%v' container", container.Name)

		if container.Name == "compute" {
			computeIndex = i
		}
	}

	if computeIndex == -1 {
		log.Printf("Couldn't find a compute container on the pod")
	} else {
		patch = append(patch, patchOperation {
			Op: "replace",
			Path: fmt.Sprintf("/spec/containers/%v/image", computeIndex),
			Value: "quay.io/quamotion/android-x86-launcher:latest",
		})
	}

	return json.Marshal(patch)
}

// main mutation process
func (whsvr *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := ar.Request

	log.Printf("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, req.UID, req.Operation, req.UserInfo)

	log.Printf("Object json: %v", string(req.Object.Raw))

	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		log.Printf("Could not unmarshal raw object: %v", err)
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	if !mutationRequired(ignoredNamespaces, &pod.ObjectMeta) {
		log.Printf("Skipping validation for %s/%s due to policy check", pod.Namespace, pod.Name)
		return &v1beta1.AdmissionResponse{
			Allowed: true,
		}
	}

	patchBytes, err := patchPod(pod)
	if err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	log.Printf("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1beta1.AdmissionResponse{
		Allowed: true,
		Patch:   patchBytes,
		PatchType: func() *v1beta1.PatchType {
			pt := v1beta1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

// Serve method for webhook server
func (whsvr *WebhookServer) serve(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}
	if len(body) == 0 {
		log.Print("empty body")
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		log.Printf("Content-Type=%s, expect application/json", contentType)
		http.Error(w, "invalid Content-Type, expect `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		log.Printf("Can't decode body: %v", err)
		admissionResponse = &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		fmt.Println(r.URL.Path)
		if r.URL.Path == "/mutate" {
			admissionResponse = whsvr.mutate(&ar)
		}
	}

	admissionReview := v1beta1.AdmissionReview{}
	if admissionResponse != nil {
		admissionReview.Response = admissionResponse
		if ar.Request != nil {
			admissionReview.Response.UID = ar.Request.UID
		}
	}

	resp, err := json.Marshal(admissionReview)
	if err != nil {
		log.Printf("Can't encode response: %v", err)
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
	}
	log.Printf("Ready to write reponse ...")
	if _, err := w.Write(resp); err != nil {
		log.Printf("Can't write response: %v", err)
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
	}
}
