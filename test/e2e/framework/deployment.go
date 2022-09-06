/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package framework

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// EchoService name of the deployment for the echo app
const EchoService = "echo"

// SlowEchoService name of the deployment for the echo app
const SlowEchoService = "slow-echo"

// AlwaysSlowEchoService name of the deployment for the echo app
const AlwaysSlowEchoService = "always-slow-echo"

// HTTPBinService name of the deployment for the httpbin app
const HTTPBinService = "httpbin"

type deploymentOptions struct {
	namespace        string
	name             string
	replicas         int
	image            string
	serviceName      string
	duplicateService bool
}

// WithDeploymentNamespace allows configuring the deployment's namespace
func WithDeploymentNamespace(n string) func(*deploymentOptions) {
	return func(o *deploymentOptions) {
		o.namespace = n
	}
}

// WithDeploymentName allows configuring the deployment's names
func WithDeploymentName(n string) func(*deploymentOptions) {
	return func(o *deploymentOptions) {
		o.name = n
	}
}

// WithDeploymentReplicas allows configuring the deployment's replicas count
func WithDeploymentReplicas(r int) func(*deploymentOptions) {
	return func(o *deploymentOptions) {
		o.replicas = r
	}
}

// WithServiceName overrides the default service name connected to the deployment. To allow multiple deployments to
// match the same service an additional tag is used for the pods and ignored by the specified service.
func WithServiceName(s string) func(*deploymentOptions) {
	return func(o *deploymentOptions) {
		o.serviceName = s
	}
}

// NewEchoDeployment creates a new single replica deployment of the echo server image in a particular namespace
func (f *Framework) NewEchoDeployment(opts ...func(*deploymentOptions)) {
	options := &deploymentOptions{
		namespace:   f.Namespace,
		name:        EchoService,
		replicas:    1,
		serviceName: "",
	}
	for _, o := range opts {
		o(options)
	}

	// If a serviceName is defined then pods from any app will be labelled such that the specified service matches them
	podLabels := make(map[string]string)
	selectorLabels := make(map[string]string)
	podLabels["app"] = options.name
	if options.serviceName == "" {
		options.serviceName = options.name
		selectorLabels["app"] = options.name
	} else {
		podLabels["service"] = options.serviceName
		selectorLabels["service"] = options.serviceName
	}

	deployment := newDeployment(options.name, options.namespace, "registry.k8s.io/ingress-nginx/e2e-test-echo@sha256:778ac6d1188c8de8ecabeddd3c37b72c8adc8c712bad2bd7a81fb23a3514934c", 80, int32(options.replicas),
		nil,
		[]corev1.VolumeMount{},
		[]corev1.Volume{},
		podLabels,
	)

	f.EnsureDeployment(deployment)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      options.serviceName,
			Namespace: options.namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(80),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Selector: selectorLabels,
		},
	}

	// If a service name is provided then don't throw an error if it already exists
	if options.serviceName == options.name {
		f.EnsureService(service)
	} else {
		f.EnsureServiceExists(service)
	}

	var err error
	// If service name is different to name then we could have more replicas than just those created by this ingress
	if options.serviceName == options.name {
		err = WaitForEndpoints(f.KubeClientSet, DefaultTimeout, options.serviceName, options.namespace, options.replicas)
	} else {
		err = WaitForMinimumEndpoints(f.KubeClientSet, DefaultTimeout, options.serviceName, options.namespace, options.replicas)
	}
	assert.Nil(ginkgo.GinkgoT(), err, "waiting for endpoints to become ready")

}

// NewSlowEchoDeployment creates a new deployment of the slow echo server image in a particular namespace.
func (f *Framework) NewSlowEchoDeployment() {
	f.NewSlowEchoDeploymentWithOptions()
	return
}

// NewSlowEchoDeploymentWithOptions creates a new deployment of the slow echo server with functional options.
func (f *Framework) NewSlowEchoDeploymentWithOptions(opts ...func(*deploymentOptions)) {
	cfg := `#
events {
	worker_connections  1024;
	multi_accept on;
}

http {
	default_type 'text/plain';
	client_max_body_size 0;

	server {
		access_log on;
		access_log /dev/stdout;

		listen 80;

		location / {
			content_by_lua_block {
				ngx.print("ok")
			}
		}

		location ~ ^/sleep/(?<sleepTime>[0-9]+)$ {
			content_by_lua_block {
				ngx.sleep(ngx.var.sleepTime)
				ngx.print("ok after " .. ngx.var.sleepTime .. " seconds")
			}
		}
	}
}
`

	f.NGINXWithConfigDeploymentWithOptions(SlowEchoService, cfg, true, opts...)
}

// NewAlwaysSlowEchoDeployment creates a new deployment of the always slow echo server. This server will always sleep
// for the specified number of milliseconds before responding regardless of path.
func (f *Framework) NewAlwaysSlowEchoDeployment(sleepMillis int) {
	f.NewAlwaysSlowEchoDeploymentWithOptions(sleepMillis)
	return
}

// NewAlwaysSlowEchoDeploymentWithOptions creates a new deployment of the always slow echo server with functional options.
// This server always sleeps for the specified number of milliseconds before responding. NOTE: values for sleepMillis
// >= 2000 will cause the deployment to fail health checks, causing false positive test failures.
func (f *Framework) NewAlwaysSlowEchoDeploymentWithOptions(sleepMillis int, opts ...func(*deploymentOptions)) {
	delay := float32(sleepMillis) * 0.001
	cfg := fmt.Sprintf(`#
events {
	worker_connections  1024;
	multi_accept on;
}

http {
	default_type 'text/plain';
	client_max_body_size 0;

	server {
		access_log on;
		access_log /dev/stdout;

		listen 80;

		location / {
			content_by_lua_block {
				ngx.sleep(%.3f)
				ngx.print("echo ok after %.3f seconds")
			}
		}
	}
}
`, delay, delay)

	f.NGINXWithConfigDeploymentWithOptions(AlwaysSlowEchoService, cfg, true, opts...)
}

func (f *Framework) GetNginxBaseImage() string {
	nginxBaseImage := os.Getenv("NGINX_BASE_IMAGE")

	if nginxBaseImage == "" {
		assert.NotEmpty(ginkgo.GinkgoT(), errors.New("NGINX_BASE_IMAGE not defined"), "NGINX_BASE_IMAGE not defined")
	}

	return nginxBaseImage
}

// NGINXDeployment creates a new simple NGINX Deployment using NGINX base image
// and passing the desired configuration
func (f *Framework) NGINXDeployment(name string, cfg string, waitendpoint bool) {
	f.NGINXDeploymentWithOptions(name, cfg, waitendpoint)
	return
}

// NGINXDeploymentWithOptions creates a new simple NGINX Deployment using NGINX base image and the desired configuration,
// with overrides applied by any supplied functional options.
func (f *Framework) NGINXDeploymentWithOptions(name string, cfg string, waitendpoint bool, opts ...func(options *deploymentOptions)) {
	cfgMap := map[string]string{
		"nginx.conf": cfg,
	}

	options := &deploymentOptions{
		namespace:   f.Namespace,
		name:        name,
		replicas:    1,
		serviceName: "",
	}
	for _, o := range opts {
		o(options)
	}
	// If a serviceName is defined then pods from any app will be labelled such that that service matches them
	podLabels := make(map[string]string)
	selectorLabels := make(map[string]string)
	podLabels["app"] = options.name
	if options.serviceName == "" {
		options.serviceName = options.name
		selectorLabels["app"] = options.name
	} else {
		podLabels["service"] = options.serviceName
		selectorLabels["service"] = options.serviceName
	}

	_, err := f.KubeClientSet.CoreV1().ConfigMaps(options.namespace).Create(context.TODO(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      options.name,
			Namespace: options.namespace,
		},
		Data: cfgMap,
	}, metav1.CreateOptions{})
	assert.Nil(ginkgo.GinkgoT(), err, "creating configmap")

	deployment := newDeployment(name, f.Namespace, f.GetNginxBaseImage(), 80, 1,
		nil,
		[]corev1.VolumeMount{
			{
				Name:      options.name,
				MountPath: "/etc/nginx/nginx.conf",
				SubPath:   "nginx.conf",
				ReadOnly:  true,
			},
		},
		[]corev1.Volume{
			{
				Name: options.name,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: options.name,
						},
					},
				},
			},
		},
		podLabels,
	)

	f.EnsureDeployment(deployment)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      options.serviceName,
			Namespace: f.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(80),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Selector: selectorLabels,
		},
	}

	// If a service name is provided then don't throw an error if it already exists
	if options.serviceName == options.name {
		f.EnsureService(service)
	} else {
		f.EnsureServiceExists(service)
	}

	if waitendpoint {
		if options.serviceName == options.name {
			err = WaitForEndpoints(f.KubeClientSet, DefaultTimeout, name, f.Namespace, 1)
		} else {
			err = WaitForMinimumEndpoints(f.KubeClientSet, DefaultTimeout, options.serviceName, f.Namespace, 1)
		}
		assert.Nil(ginkgo.GinkgoT(), err, "waiting for endpoints to become ready")
	}
}

// NGINXWithConfigDeployment creates an NGINX deployment using a configmap containing the nginx.conf configuration
func (f *Framework) NGINXWithConfigDeployment(name string, cfg string) {
	f.NGINXWithConfigDeploymentWithOptions(name, cfg, true)
}

// NGINXWithConfigDeploymentWithOptions creates an NGINX deployment with nginx.conf override and functional options
func (f *Framework) NGINXWithConfigDeploymentWithOptions(name string, cfg string, waitendpoint bool, opts ...func(*deploymentOptions)) {
	f.NGINXDeploymentWithOptions(name, cfg, waitendpoint, opts...)
}

// NewGRPCBinDeployment creates a new deployment of the
// moul/grpcbin image for GRPC tests
func (f *Framework) NewGRPCBinDeployment() {
	name := "grpcbin"

	probe := &corev1.Probe{
		InitialDelaySeconds: 1,
		PeriodSeconds:       1,
		SuccessThreshold:    1,
		TimeoutSeconds:      1,
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.FromInt(9000),
			},
		},
	}

	sel := map[string]string{
		"app": name,
	}

	f.EnsureDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: NewInt32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: sel,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: sel,
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: NewInt64(0),
					Containers: []corev1.Container{
						{
							Name:  name,
							Image: "moul/grpcbin",
							Env:   []corev1.EnvVar{},
							Ports: []corev1.ContainerPort{
								{
									Name:          "insecure",
									ContainerPort: 9000,
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "secure",
									ContainerPort: 9001,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							ReadinessProbe: probe,
							LivenessProbe:  probe,
						},
					},
				},
			},
		},
	})

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "insecure",
					Port:       9000,
					TargetPort: intstr.FromInt(9000),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "secure",
					Port:       9001,
					TargetPort: intstr.FromInt(9001),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Selector: sel,
		},
	}

	f.EnsureService(service)

	err := WaitForEndpoints(f.KubeClientSet, DefaultTimeout, name, f.Namespace, 1)
	assert.Nil(ginkgo.GinkgoT(), err, "waiting for endpoints to become ready")
}

func newDeployment(name, namespace, image string, port int32, replicas int32, command []string,
	volumeMounts []corev1.VolumeMount, volumes []corev1.Volume, podLabels map[string]string) *appsv1.Deployment {

	if podLabels == nil {
		podLabels = map[string]string{
			"app": name,
		}
	}

	probe := &corev1.Probe{
		InitialDelaySeconds: 2,
		PeriodSeconds:       1,
		SuccessThreshold:    1,
		TimeoutSeconds:      2,
		FailureThreshold:    6,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Port: intstr.FromString("http"),
				Path: "/",
			},
		},
	}

	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: NewInt32(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: NewInt64(0),
					Containers: []corev1.Container{
						{
							Name:  name,
							Image: image,
							Env:   []corev1.EnvVar{},
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: port,
								},
							},
							ReadinessProbe: probe,
							LivenessProbe:  probe,
							VolumeMounts:   volumeMounts,
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	if len(command) > 0 {
		d.Spec.Template.Spec.Containers[0].Command = command
	}

	return d
}

// NewHttpbinDeployment creates a new single replica deployment of the httpbin image in a particular namespace.
func (f *Framework) NewHttpbinDeployment() {
	f.NewDeployment(HTTPBinService, "registry.k8s.io/ingress-nginx/e2e-test-httpbin@sha256:c6372ef57a775b95f18e19d4c735a9819f2e7bb4641e5e3f27287d831dfeb7e8", 80, 1)
}

// NewDeployment creates a new deployment in a particular namespace.
func (f *Framework) NewDeployment(name, image string, port int32, replicas int32) {
	deployment := newDeployment(name, f.Namespace, image, port, replicas, nil, nil, nil, nil)

	f.EnsureDeployment(deployment)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(int(port)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": name,
			},
		},
	}

	f.EnsureService(service)

	err := WaitForEndpoints(f.KubeClientSet, DefaultTimeout, name, f.Namespace, int(replicas))
	assert.Nil(ginkgo.GinkgoT(), err, "waiting for endpoints to become ready")
}

// DeleteDeployment deletes a deployment with a particular name and waits for the pods to be deleted
func (f *Framework) DeleteDeployment(name string) error {
	d, err := f.KubeClientSet.AppsV1().Deployments(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	assert.Nil(ginkgo.GinkgoT(), err, "getting deployment")

	grace := int64(0)
	err = f.KubeClientSet.AppsV1().Deployments(f.Namespace).Delete(context.TODO(), name, metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
	})
	assert.Nil(ginkgo.GinkgoT(), err, "deleting deployment")

	return waitForPodsDeleted(f.KubeClientSet, 2*time.Minute, f.Namespace, metav1.ListOptions{
		LabelSelector: labelSelectorToString(d.Spec.Selector.MatchLabels),
	})
}

// ScaleDeploymentToZero scales a deployment with a particular name and waits for the pods to be deleted
func (f *Framework) ScaleDeploymentToZero(name string) {
	d, err := f.KubeClientSet.AppsV1().Deployments(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	assert.Nil(ginkgo.GinkgoT(), err, "getting deployment")
	assert.NotNil(ginkgo.GinkgoT(), d, "expected a deployment but none returned")

	d.Spec.Replicas = NewInt32(0)

	d, err = f.KubeClientSet.AppsV1().Deployments(f.Namespace).Update(context.TODO(), d, metav1.UpdateOptions{})
	assert.Nil(ginkgo.GinkgoT(), err, "getting deployment")
	assert.NotNil(ginkgo.GinkgoT(), d, "expected a deployment but none returned")

	err = WaitForEndpoints(f.KubeClientSet, DefaultTimeout, name, f.Namespace, 0)
	assert.Nil(ginkgo.GinkgoT(), err, "waiting for no endpoints")
}

// UpdateIngressControllerDeployment updates the ingress-nginx deployment
func (f *Framework) UpdateIngressControllerDeployment(fn func(deployment *appsv1.Deployment) error) error {
	err := UpdateDeployment(f.KubeClientSet, f.Namespace, "nginx-ingress-controller", 1, fn)
	if err != nil {
		return err
	}

	return f.updateIngressNGINXPod()
}
