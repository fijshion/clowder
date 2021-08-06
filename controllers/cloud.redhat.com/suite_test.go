/*


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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RedHatInsights/clowder/apis/cloud.redhat.com/v1alpha1/common"
	"go.uber.org/zap"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	crd "github.com/RedHatInsights/clowder/apis/cloud.redhat.com/v1alpha1"
	"github.com/RedHatInsights/clowder/controllers/cloud.redhat.com/config"
	p "github.com/RedHatInsights/clowder/controllers/cloud.redhat.com/providers"
	strimzi "github.com/fijshion/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	// +kubebuilder:scaffold:imports
)

var k8sClient client.Client
var testEnv *envtest.Environment
var logger *zap.Logger

func TestMain(m *testing.M) {
	// call flag.Parse() here if TestMain uses flags
	ctrl.SetLogger(ctrlzap.New(ctrlzap.UseDevMode(true)))
	logger, _ = zap.NewProduction()
	defer logger.Sync()
	logger.Info("bootstrapping test environment")

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),  // generated by controller-gen
			filepath.Join("..", "..", "config", "crd", "static"), // added to the project manually
		},
	}

	cfg, err := testEnv.Start()

	if err != nil {
		logger.Fatal("Error starting test env", zap.Error(err))
	}

	if cfg == nil {
		logger.Fatal("env config was returned nil")
	}

	err = crd.AddToScheme(clientgoscheme.Scheme)

	if err != nil {
		logger.Fatal("Failed to add scheme", zap.Error(err))
	}

	err = strimzi.AddToScheme(clientgoscheme.Scheme)

	if err != nil {
		logger.Fatal("Failed to add scheme", zap.Error(err))
	}

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})

	if err != nil {
		logger.Fatal("Failed to create k8s client", zap.Error(err))
	}

	if k8sClient == nil {
		logger.Fatal("k8sClient was returned nil", zap.Error(err))
	}

	ctx := context.Background()

	nsSpec := &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kafka"}}
	k8sClient.Create(ctx, nsSpec)

	stopManager, cancel := context.WithCancel(context.Background())
	go Run(":8080", ":8081", false, testEnv.Config, stopManager, false)

	for i := 1; i <= 50; i++ {
		resp, err := http.Get("http://localhost:8080/metrics")

		if err == nil && resp.StatusCode == 200 {
			logger.Info("Manager ready", zap.Int("duration", 100*i))
			break
		}

		if i == 50 {
			if err != nil {
				logger.Fatal("Failed to fetch to metrics for manager after 5s", zap.Error(err))
			}

			logger.Fatal("Failed to get 200 result for metrics", zap.Int("status", resp.StatusCode))
		}

		time.Sleep(100 * time.Millisecond)
	}

	retCode := m.Run()
	logger.Info("Stopping test env...")
	cancel()
	err = testEnv.Stop()

	if err != nil {
		logger.Fatal("Failed to tear down env", zap.Error(err))
	}
	os.Exit(retCode)
}

func applyKafkaStatus(t *testing.T, ch chan int) {
	ctx := context.Background()
	nn := types.NamespacedName{
		Name:      "kafka",
		Namespace: "kafka",
	}
	host := "kafka-bootstrap.kafka.svc"
	listenerType := "plain"
	kport := int32(9092)

	// this loop will run for 60sec max
	for i := 1; i < 1200; i++ {
		if t.Failed() {
			break
		}
		t.Logf("Loop in applyKafkaStatus")
		time.Sleep(50 * time.Millisecond)

		// set a mock status on strimzi Kafka cluster
		cluster := strimzi.Kafka{}
		err := k8sClient.Get(ctx, nn, &cluster)

		if err != nil {
			t.Logf(err.Error())
			continue
		}

		cluster.Status = &strimzi.KafkaStatus{
			Conditions: []strimzi.KafkaStatusConditionsElem{{
				Status: common.StringPtr("True"),
				Type:   common.StringPtr("Ready"),
			}},
			Listeners: []strimzi.KafkaStatusListenersElem{{
				Type: &listenerType,
				Addresses: []strimzi.KafkaStatusListenersElemAddressesElem{{
					Host: &host,
					Port: &kport,
				}},
			}},
		}
		t.Logf("Applying kafka status")
		err = k8sClient.Status().Update(ctx, &cluster)

		if err != nil {
			t.Logf(err.Error())
			continue
		}

		// set a mock status on strimzi KafkaConnect cluster
		connectCluster := strimzi.KafkaConnect{}
		nn := types.NamespacedName{
			Name:      "kafka",
			Namespace: "kafka",
		}
		err = k8sClient.Get(ctx, nn, &connectCluster)

		if err != nil {
			t.Logf(err.Error())
			continue
		}

		connectCluster.Status = &strimzi.KafkaConnectStatus{
			Conditions: []strimzi.KafkaConnectStatusConditionsElem{{
				Status: common.StringPtr("True"),
				Type:   common.StringPtr("Ready"),
			}},
		}
		t.Logf("Applying kafka connect status")
		err = k8sClient.Status().Update(ctx, &connectCluster)

		if err != nil {
			t.Logf(err.Error())
			continue
		}

		break
	}

	ch <- 0
}

func createCloudwatchSecret(cwData *map[string]string) error {
	cloudwatch := core.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloudwatch",
			Namespace: "default",
		},
		StringData: *cwData,
	}

	return k8sClient.Create(context.Background(), &cloudwatch)
}

func createCRs(name types.NamespacedName) (*crd.ClowdEnvironment, *crd.ClowdApp, error) {
	ctx := context.Background()

	objMeta := metav1.ObjectMeta{
		Name:      name.Name,
		Namespace: name.Namespace,
	}

	env := crd.ClowdEnvironment{
		ObjectMeta: objMeta,
		Spec: crd.ClowdEnvironmentSpec{
			Providers: crd.ProvidersConfig{
				Kafka: crd.KafkaConfig{
					Mode: "operator",
					Cluster: crd.KafkaClusterConfig{
						Name:      "kafka",
						Namespace: "kafka",
						Replicas:  5,
					},
				},
				Database: crd.DatabaseConfig{
					Mode: "local",
				},
				Logging: crd.LoggingConfig{
					Mode: "app-interface",
				},
				ObjectStore: crd.ObjectStoreConfig{
					Mode: "app-interface",
				},
				InMemoryDB: crd.InMemoryDBConfig{
					Mode: "redis",
				},
				Web: crd.WebConfig{
					Port: int32(8000),
					Mode: "none",
				},
				Metrics: crd.MetricsConfig{
					Port: int32(9000),
					Path: "/metrics",
					Mode: "none",
				},
				FeatureFlags: crd.FeatureFlagsConfig{
					Mode: "none",
				},
				Testing: crd.TestingConfig{
					ConfigAccess:   "environment",
					K8SAccessLevel: "edit",
					Iqe: crd.IqeConfig{
						ImageBase: "quay.io/cloudservices/iqe-tests",
					},
				},
			},
			TargetNamespace: objMeta.Namespace,
		},
	}

	replicas := int32(32)
	partitions := int32(5)
	dbVersion := int32(12)
	topicName := "inventory"

	kafkaTopics := []crd.KafkaTopicSpec{
		{
			TopicName:  topicName,
			Partitions: partitions,
			Replicas:   replicas,
		},
		{
			TopicName: fmt.Sprintf("%s-default-values", topicName),
		},
	}

	app := crd.ClowdApp{
		ObjectMeta: objMeta,
		Spec: crd.ClowdAppSpec{
			Deployments: []crd.Deployment{{
				PodSpec: crd.PodSpec{
					Image: "test:test",
				},
				Name: "testpod",
			}},
			EnvName:     env.Name,
			KafkaTopics: kafkaTopics,
			Database: crd.DatabaseSpec{
				Version: &dbVersion,
				Name:    "test",
			},
		},
	}

	err := k8sClient.Create(ctx, &env)

	if err != nil {
		return &env, &app, err
	}

	err = k8sClient.Create(ctx, &app)

	return &env, &app, err
}

func fetchConfig(name types.NamespacedName) (*config.AppConfig, error) {

	secretConfig := core.Secret{}
	jsonContent := config.AppConfig{}

	err := fetchWithDefaults(name, &secretConfig)

	if err != nil {
		return &jsonContent, err
	}

	err = json.Unmarshal(secretConfig.Data["cdappconfig.json"], &jsonContent)

	return &jsonContent, err
}

func TestObjectCache(t *testing.T) {
	oCache := p.NewObjectCache(context.Background(), k8sClient, scheme)

	nn := types.NamespacedName{
		Name:      "test-service",
		Namespace: "default",
	}

	s := core.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nn.Name,
			Namespace: nn.Namespace,
		},
		Spec: core.ServiceSpec{
			Ports: []core.ServicePort{{
				Name: "port-01",
				Port: 1234,
			}},
		},
	}

	SingleIdent := p.ResourceIdentSingle{
		Provider: "TEST",
		Purpose:  "MAIN",
		Type:     &core.Service{},
	}

	err := oCache.Create(SingleIdent, nn, &s)
	if err != nil {
		t.Error(err)
		return
	}

	obtainedService := core.Service{}

	err = oCache.Get(SingleIdent, &obtainedService)
	if err != nil {
		t.Error(err)
		return
	}

	if obtainedService.Spec.Ports[0].Port != 1234 {
		t.Errorf("Obtained service did not have port 1234")
		return
	}

	obtainedService.Spec.Ports[0].Port = 2345

	err = oCache.Update(SingleIdent, &obtainedService)
	if err != nil {
		t.Error(err)
		return
	}

	updatedService := core.Service{}

	err = oCache.Get(SingleIdent, &updatedService)
	if err != nil {
		t.Error(err)
		return
	}

	if updatedService.Spec.Ports[0].Port != 2345 {
		t.Errorf("Updated service port was not updated")
		return
	}

	MultiIdent := p.ResourceIdentMulti{
		Provider: "TEST",
		Purpose:  "MULTI",
		Type:     &core.Service{},
	}

	sm := core.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nn.Name + "-multi",
			Namespace: nn.Namespace,
		},
		Spec: core.ServiceSpec{
			Ports: []core.ServicePort{{
				Name: "port-01",
				Port: 5432,
			}},
		},
	}

	err = oCache.Create(MultiIdent, nn, &sm)
	if err != nil {
		t.Error(err)
		return
	}

	sList := core.ServiceList{}
	err = oCache.List(MultiIdent, &sList)

	if err != nil {
		t.Error(err)
		return
	}

	for _, i := range sList.Items {
		if i.Spec.Ports[0].Port != 5432 {
			t.Errorf("Item not found in list")
			return
		}
	}

	err = oCache.ApplyAll()

	if err != nil {
		t.Error(err)
		return
	}

	clientService := core.Service{}
	if err = k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: "default",
		Name:      "test-service",
	}, &clientService); err != nil {
		t.Error(err)
		return
	}

	if clientService.Spec.Ports[0].Port != 2345 {
		t.Errorf("Retrieved object has wrong port")
		return
	}

	clientServiceMulti := core.Service{}
	if err = k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: "default",
		Name:      "test-service-multi",
	}, &clientServiceMulti); err != nil {
		t.Error(err)
		return
	}

	if clientServiceMulti.Spec.Ports[0].Port != 5432 {
		t.Errorf("Retrieved object has wrong port")
		return
	}

	TemplateIdent := p.ResourceIdentSingle{
		Provider: "TEST",
		Purpose:  "TEMPLATE",
		Type:     &core.Pod{},
	}

	tnn := types.NamespacedName{
		Name:      "template",
		Namespace: "template-namespace",
	}
	service := &core.Service{}

	if err := oCache.Create(TemplateIdent, tnn, service); err == nil {
		t.Fatal(err)
		t.Fatal("Did not error when should have: cache create")
	}

	if err := oCache.Update(TemplateIdent, service); err == nil {
		t.Fatal("Did not error when should have: cache update")
	}
}

func TestCreateClowdApp(t *testing.T) {
	logger.Info("Creating ClowdApp")

	clowdAppNN := types.NamespacedName{
		Name:      "test",
		Namespace: "default",
	}

	cwData := map[string]string{
		"aws_access_key_id":     "key_id",
		"aws_secret_access_key": "secret",
		"log_group_name":        "default",
		"aws_region":            "us-east-1",
	}

	err := createCloudwatchSecret(&cwData)

	if err != nil {
		t.Error(err)
		return
	}

	ch := make(chan int)

	go applyKafkaStatus(t, ch)

	env, app, err := createCRs(clowdAppNN)

	if err != nil {
		t.Error(err)
		return
	}

	<-ch // wait for kafka status to be applied

	labels := map[string]string{
		"app": app.Name,
		"pod": fmt.Sprintf("%s-%s", app.Name, app.Spec.Deployments[0].Name),
	}

	// See if Deployment is created

	d := apps.Deployment{}

	appnn := types.NamespacedName{
		Name:      fmt.Sprintf("%s-%s", app.Name, app.Spec.Deployments[0].Name),
		Namespace: clowdAppNN.Namespace,
	}
	err = fetchWithDefaults(appnn, &d)

	if err != nil {
		t.Error(err)
		return
	}

	if !mapEq(d.Labels, labels) {
		t.Errorf("Deployment label mismatch %v; expected %v", d.Labels, labels)
	}

	antiAffinity := d.Spec.Template.Spec.Affinity.PodAntiAffinity
	terms := antiAffinity.PreferredDuringSchedulingIgnoredDuringExecution

	if len(terms) != 2 {
		t.Errorf("Incorrect number of anti-affinity terms: %d; expected 2", len(terms))
	}

	c := d.Spec.Template.Spec.Containers[0]

	if c.Image != app.Spec.Deployments[0].PodSpec.Image {
		t.Errorf("Bad image spec %s; expected %s", c.Image, app.Spec.Deployments[0].PodSpec.Image)
	}

	// See if Secret is mounted

	found := false
	for _, mount := range c.VolumeMounts {
		if mount.Name == "config-secret" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Deployment %s does not have the config volume mounted", d.Name)
		return
	}

	s := core.Service{}

	err = fetchWithDefaults(appnn, &s)

	if err != nil {
		t.Error(err)
		return
	}

	if !mapEq(s.Labels, labels) {
		t.Errorf("Service label mismatch %v; expected %v", s.Labels, labels)
	}

	// Simple test for service right expects there only to be the metrics port
	if len(s.Spec.Ports) > 1 {
		t.Errorf("Bad port count %d; expected 1", len(s.Spec.Ports))
	}

	if s.Spec.Ports[0].Port != env.Spec.Providers.Metrics.Port {
		t.Errorf("Bad port created %d; expected %d", s.Spec.Ports[0].Port, env.Spec.Providers.Metrics.Port)
	}

	jsonContent, err := fetchConfig(clowdAppNN)

	if err != nil {
		t.Error(err)
		return
	}

	kafkaValidation(t, env, app, jsonContent, clowdAppNN)

	clowdWatchValidation(t, jsonContent, cwData)
}

func kafkaValidation(t *testing.T, env *crd.ClowdEnvironment, app *crd.ClowdApp, jsonContent *config.AppConfig, clowdAppNN types.NamespacedName) {
	// Kafka validation

	topicWithPartitionsReplicasName := "inventory"
	topicWithPartitionsReplicasNamespacedName := types.NamespacedName{
		Namespace: env.Spec.Providers.Kafka.Cluster.Namespace,
		Name:      topicWithPartitionsReplicasName,
	}

	topicNoPartitionsReplicasName := "inventory-default-values"
	topicNoPartitionsReplicasNamespacedName := types.NamespacedName{
		Namespace: env.Spec.Providers.Kafka.Cluster.Namespace,
		Name:      topicNoPartitionsReplicasName,
	}

	for i, kafkaTopic := range app.Spec.KafkaTopics {
		actual, expected := jsonContent.Kafka.Topics[i].RequestedName, kafkaTopic.TopicName
		if actual != expected {
			t.Errorf("Wrong topic name set on app's config; got %s, want %s", actual, expected)
		}

		actual = jsonContent.Kafka.Topics[i].Name
		expected = kafkaTopic.TopicName
		if actual != expected {
			t.Errorf("Wrong generated topic name set on app's config; got %s, want %s", actual, expected)
		}
	}

	if len(jsonContent.Kafka.Brokers[0].Hostname) == 0 {
		t.Error("Kafka broker hostname is not set")
		return
	}

	for _, topic := range []types.NamespacedName{topicWithPartitionsReplicasNamespacedName, topicNoPartitionsReplicasNamespacedName} {
		fetchedTopic := strimzi.KafkaTopic{}

		// fetch topic, make sure it was provisioned
		if err := fetchWithDefaults(topic, &fetchedTopic); err != nil {
			t.Fatalf("error fetching topic '%s': %v", topic.Name, err)
		}
		if fetchedTopic.Spec == nil {
			t.Fatalf("KafkaTopic '%s' not provisioned in namespace", topic.Name)
		}

		// check that configured partitions/replicas matches
		expectedReplicas := int32(0)
		expectedPartitions := int32(0)
		if topic.Name == topicWithPartitionsReplicasName {
			expectedReplicas = int32(5)
			expectedPartitions = int32(5)
		}
		if topic.Name == topicNoPartitionsReplicasName {
			expectedReplicas = int32(3)
			expectedPartitions = int32(3)
		}
		if fetchedTopic.Spec.Replicas != expectedReplicas {
			t.Errorf("Bad topic replica count for '%s': %d; expected %d", topic.Name, fetchedTopic.Spec.Replicas, expectedReplicas)
		}
		if fetchedTopic.Spec.Partitions != expectedPartitions {
			t.Errorf("Bad topic replica count for '%s': %d; expected %d", topic.Name, fetchedTopic.Spec.Partitions, expectedPartitions)
		}
	}
}

func clowdWatchValidation(t *testing.T, jsonContent *config.AppConfig, cwData map[string]string) {
	// Cloudwatch validation
	cwConfigVals := map[string]string{
		"aws_access_key_id":     jsonContent.Logging.Cloudwatch.AccessKeyId,
		"aws_secret_access_key": jsonContent.Logging.Cloudwatch.SecretAccessKey,
		"log_group_name":        jsonContent.Logging.Cloudwatch.LogGroup,
		"aws_region":            jsonContent.Logging.Cloudwatch.Region,
	}

	for key, val := range cwData {
		if val != cwConfigVals[key] {
			t.Errorf("Wrong cloudwatch config value %s; expected %s", cwConfigVals[key], val)
			return
		}
	}
}

func fetchWithDefaults(name types.NamespacedName, resource client.Object) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return fetch(ctx, name, resource, 20, 500*time.Millisecond)
}

func fetch(ctx context.Context, name types.NamespacedName, resource client.Object, retryCount int, sleepTime time.Duration) error {
	var err error

	for i := 1; i <= retryCount; i++ {
		err = k8sClient.Get(ctx, name, resource)

		if err == nil {
			return nil
		} else if !k8serr.IsNotFound(err) {
			return err
		}

		time.Sleep(sleepTime)
	}

	return err
}

func mapEq(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, va := range a {
		vb, ok := b[k]

		if !ok {
			return false
		}

		if va != vb {
			return false
		}
	}

	return true
}
