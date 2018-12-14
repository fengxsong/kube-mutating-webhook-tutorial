package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/golang/glog"

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

const (
	admissionWebhookAnnotationInjectKey = "adjust-tz.k8s.example.io/inject"
	admissionWebhookAnnotationStatusKey = "adjust-tz.k8s.example.io/status"
)

// WebhookServer webhook server
type WebhookServer struct {
	ignoredNamespaces []string // namespaces ignored
	tz                tzFile
	server            *http.Server
}

// WhSvrParameters webhook server parameters
type WhSvrParameters struct {
	port              int      // webhook server port
	certFile          string   // path to the x509 certificate for https
	keyFile           string   // path to the x509 private key matching `CertFile`
	ignoredNamespaces []string // namespaces ignored
	tz                string   // timezone file to mount
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

// (https://github.com/kubernetes/kubernetes/issues/57982)
// func applyDefaultsWorkaround(containers []corev1.Container, volumes []corev1.Volume) {
// 	defaulter.Default(&corev1.Pod{
// 		Spec: corev1.PodSpec{
// 			Containers: containers,
// 			Volumes:    volumes,
// 		},
// 	})
// }

// Check whether the target resoured need to be mutated
func mutationRequired(ignoredList []string, metadata *metav1.ObjectMeta) bool {
	// skip special kubernete system namespaces
	for _, namespace := range ignoredList {
		if metadata.Namespace == namespace {
			glog.Infof("Skip mutation for %v for it' in special namespace:%v", metadata.Name, metadata.Namespace)
			return false
		}
	}

	annotations := metadata.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	status := annotations[admissionWebhookAnnotationStatusKey]

	// determine whether to perform mutation based on annotation for the target resource
	var required bool
	if strings.ToLower(status) == "injected" {
		required = false
	} else {
		switch strings.ToLower(annotations[admissionWebhookAnnotationInjectKey]) {
		default:
			required = true
		case "n", "no", "false", "off":
			required = false
		}
	}

	glog.V(4).Infof("Mutation policy for %v/%v: status: %q required:%v", metadata.Namespace, metadata.Name, status, required)
	return required
}

type tzFile struct {
	name      string
	hostPath  string
	mountPath string
}

func tzHostPathInjected(volumes []corev1.Volume, tz tzFile) bool {
	for _, v := range volumes {
		if v.Name == tz.name || (v.HostPath != nil && v.HostPath.Path == tz.hostPath) {
			return true
		}
	}
	return false
}

func tzFileMounted(volumeMounts []corev1.VolumeMount, tz tzFile) bool {
	for _, v := range volumeMounts {
		if v.Name == tz.name || v.MountPath == tz.mountPath {
			return true
		}
	}
	return false
}

func updateVolume(volumes []corev1.Volume, tz tzFile, basePath string) (patch []patchOperation) {
	if !tzHostPathInjected(volumes, tz) {
		volumes = append(volumes, corev1.Volume{
			Name: tz.name,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: tz.hostPath},
			},
		})
	}
	patch = append(patch, patchOperation{
		Op:    "replace",
		Path:  basePath,
		Value: volumes,
	})
	return
}

func updateContainers(containers []corev1.Container, tz tzFile, basePath string) (patch []patchOperation) {
	var patchedContainers []corev1.Container
	for _, c := range containers {
		if c.VolumeMounts == nil {
			c.VolumeMounts = []corev1.VolumeMount{}
		}
		if !tzFileMounted(c.VolumeMounts, tz) {
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
				Name:      tz.name,
				ReadOnly:  true,
				MountPath: tz.mountPath,
			})
		}
		patchedContainers = append(patchedContainers, c)
	}
	patch = append(patch, patchOperation{
		Op:    "replace",
		Path:  basePath,
		Value: patchedContainers,
	})
	return
}

func updateAnnotation(target map[string]string, added map[string]string) (patch []patchOperation) {
	for key, value := range added {
		if target == nil || target[key] == "" {
			target = map[string]string{}
			patch = append(patch, patchOperation{
				Op:   "add",
				Path: "/metadata/annotations",
				Value: map[string]string{
					key: value,
				},
			})
		} else {
			patch = append(patch, patchOperation{
				Op:    "replace",
				Path:  "/metadata/annotations/" + key,
				Value: value,
			})
		}
	}
	return patch
}

// create mutation patch for resoures
func createPatch(pod *corev1.Pod, tz tzFile, annotations map[string]string) ([]byte, error) {
	var patch []patchOperation

	patch = append(patch, updateContainers(pod.Spec.Containers, tz, "/spec/containers")...)
	patch = append(patch, updateVolume(pod.Spec.Volumes, tz, "/spec/volumes")...)
	patch = append(patch, updateAnnotation(pod.Annotations, annotations)...)

	return json.Marshal(patch)
}

// main mutation process
func (whsvr *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := ar.Request
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		glog.Errorf("Could not unmarshal raw object: %v", err)
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	glog.V(3).Infof("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, pod.Name, req.UID, req.Operation, req.UserInfo)

	// determine whether to perform mutation
	if !mutationRequired(whsvr.ignoredNamespaces, &pod.ObjectMeta) {
		glog.Infof("Skipping mutation for %s/%s due to policy check", pod.Namespace, pod.Name)
		return &v1beta1.AdmissionResponse{
			Allowed: true,
		}
	}

	// Workaround: https://github.com/kubernetes/kubernetes/issues/57982
	// applyDefaultsWorkaround(whsvr.sidecarConfig.Containers, whsvr.sidecarConfig.Volumes)
	annotations := map[string]string{admissionWebhookAnnotationStatusKey: "injected"}
	patchBytes, err := createPatch(&pod, whsvr.tz, annotations)
	if err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	glog.V(3).Infof("AdmissionResponse: patch=%v\n", string(patchBytes))
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
		glog.Error("empty body")
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		glog.Errorf("Content-Type=%s, expect application/json", contentType)
		http.Error(w, "invalid Content-Type, expect `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		glog.Errorf("Can't decode body: %v", err)
		admissionResponse = &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		admissionResponse = whsvr.mutate(&ar)
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
		glog.Errorf("Can't encode response: %v", err)
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
	}

	if _, err := w.Write(resp); err != nil {
		glog.Errorf("Can't write response: %v", err)
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
	}
}
