package charttest

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/engine"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/yaml"
)

const (
	chartDir    = "../cisid"
	releaseName = "ci"
	namespace   = "build"
)

// kubeVersion is a cluster version the chart supports (Chart.yaml kubeVersion).
var kubeVersion = common.KubeVersion{Version: "v1.33.0", Major: "1", Minor: "33"}

// required are the values an install cannot do without.
func required() map[string]any {
	return map[string]any{
		"secrets": map[string]any{
			"webhookSecret": "test-webhook-secret",
			"agentToken":    "test-agent-token-0123456789",
		},
		"postgresql": map[string]any{
			"auth": map[string]any{"password": "pg-pass_0123"},
		},
	}
}

// with returns vals with value set at the dotted path, e.g. "server.tls.secretName".
func with(vals map[string]any, path string, value any) map[string]any {
	m := vals
	keys := strings.Split(path, ".")
	for _, k := range keys[:len(keys)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[keys[len(keys)-1]] = value
	return vals
}

// renderFiles renders the chart as "helm install ci --namespace build" would.
func renderFiles(vals map[string]any) (map[string]string, error) {
	chrt, err := loader.Load(chartDir)
	if err != nil {
		return nil, fmt.Errorf("load chart: %w", err)
	}
	caps := common.DefaultCapabilities.Copy()
	caps.KubeVersion = kubeVersion
	opts := common.ReleaseOptions{Name: releaseName, Namespace: namespace, Revision: 1, IsInstall: true}
	renderVals, err := util.ToRenderValuesWithSchemaValidation(chrt, vals, opts, caps, false)
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}
	files, err := engine.Render(chrt, renderVals)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	return files, nil
}

// manifests are the rendered objects by name, decoded into their API types.
type manifests struct {
	deployments  map[string]appsv1.Deployment
	statefulSets map[string]appsv1.StatefulSet
	services     map[string]corev1.Service
	secrets      map[string]corev1.Secret
	accounts     map[string]corev1.ServiceAccount
	pods         map[string]corev1.Pod
	ingresses    map[string]networkingv1.Ingress
	policies     map[string]networkingv1.NetworkPolicy
	// others are objects of kinds without Go types here, e.g. ServiceMonitor.
	others map[string]map[string]any
	notes  string
}

var docSeparator = regexp.MustCompile(`(?m)^---[ \t]*$`)

// render renders the chart, decodes every object strictly (an unknown or
// misspelt field is an error) and checks the references between them.
func render(t *testing.T, vals map[string]any) manifests {
	t.Helper()
	files, err := renderFiles(vals)
	require.NoError(t, err)

	m := manifests{
		deployments:  map[string]appsv1.Deployment{},
		statefulSets: map[string]appsv1.StatefulSet{},
		services:     map[string]corev1.Service{},
		secrets:      map[string]corev1.Secret{},
		accounts:     map[string]corev1.ServiceAccount{},
		pods:         map[string]corev1.Pod{},
		ingresses:    map[string]networkingv1.Ingress{},
		policies:     map[string]networkingv1.NetworkPolicy{},
		others:       map[string]map[string]any{},
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if strings.HasSuffix(name, "NOTES.txt") {
			m.notes = files[name]
			continue
		}
		for _, doc := range docSeparator.Split(files[name], -1) {
			if strings.TrimSpace(stripComments(doc)) == "" {
				continue
			}
			m.add(t, name, []byte(doc))
		}
	}
	m.check(t, existingSecret(vals))
	return m
}

func stripComments(doc string) string {
	var b strings.Builder
	for _, line := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func existingSecret(vals map[string]any) string {
	s, _ := vals["secrets"].(map[string]any)
	name, _ := s["existingSecret"].(string)
	return name
}

func (m *manifests) add(t *testing.T, file string, doc []byte) {
	t.Helper()
	var head struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	require.NoError(t, yaml.Unmarshal(doc, &head), file)
	require.NotEmpty(t, head.Kind, "%s: object without a kind", file)
	name := head.Metadata.Name
	where := fmt.Sprintf("%s: %s %s", file, head.Kind, name)

	switch head.Kind {
	case "Deployment":
		decodeInto(t, where, doc, m.deployments, name)
	case "StatefulSet":
		decodeInto(t, where, doc, m.statefulSets, name)
	case "Service":
		decodeInto(t, where, doc, m.services, name)
	case "Secret":
		decodeInto(t, where, doc, m.secrets, name)
	case "ServiceAccount":
		decodeInto(t, where, doc, m.accounts, name)
	case "Pod":
		decodeInto(t, where, doc, m.pods, name)
	case "Ingress":
		decodeInto(t, where, doc, m.ingresses, name)
	case "NetworkPolicy":
		decodeInto(t, where, doc, m.policies, name)
	default:
		key := head.Kind + "/" + name
		require.NotContains(t, m.others, key, "%s rendered twice", where)
		var obj map[string]any
		require.NoError(t, yaml.Unmarshal(doc, &obj), where)
		m.others[key] = obj
	}
}

// decodeInto decodes doc as the API server does: field names match case
// sensitively, and unknown or duplicate fields are errors.
func decodeInto[T any](t *testing.T, where string, doc []byte, into map[string]T, name string) {
	t.Helper()
	require.NotContains(t, into, name, "%s rendered twice", where)
	data, err := yaml.YAMLToJSONStrict(doc)
	require.NoError(t, err, where)
	var obj T
	strictErrs, err := kjson.UnmarshalStrict(data, &obj, kjson.DisallowDuplicateFields, kjson.DisallowUnknownFields)
	require.NoError(t, err, where)
	require.Empty(t, strictErrs, where)
	into[name] = obj
}

// workload is a pod template and where it comes from.
type workload struct {
	name   string
	labels map[string]string
	spec   corev1.PodSpec
	// claims are the names of volume claim templates, usable as volumes.
	claims []string
}

func (m manifests) workloads() []workload {
	var ws []workload
	for name, d := range m.deployments {
		ws = append(ws, workload{name: "Deployment " + name, labels: d.Spec.Template.Labels, spec: d.Spec.Template.Spec})
	}
	for name, s := range m.statefulSets {
		var claims []string
		for _, c := range s.Spec.VolumeClaimTemplates {
			claims = append(claims, c.Name)
		}
		ws = append(ws, workload{name: "StatefulSet " + name, labels: s.Spec.Template.Labels, spec: s.Spec.Template.Spec, claims: claims})
	}
	for name, p := range m.pods {
		ws = append(ws, workload{name: "Pod " + name, labels: p.Labels, spec: p.Spec})
	}
	return ws
}

// selects reports whether some workload's pods carry all of selector's labels.
func (m manifests) selects(selector map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for _, w := range m.workloads() {
		if isSubset(selector, w.labels) {
			return true
		}
	}
	return false
}

func isSubset(sub, set map[string]string) bool {
	for k, v := range sub {
		if set[k] != v {
			return false
		}
	}
	return true
}

var envRef = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)

// check verifies what the API server and kubelet would only find out at
// install or run time.
func (m manifests) check(t *testing.T, existingSecret string) {
	t.Helper()
	checkMeta := func(kind, name string, labels map[string]string) {
		require.Empty(t, validation.IsDNS1123Subdomain(name), "%s name %q", kind, name)
		for k, v := range labels {
			require.Empty(t, validation.IsQualifiedName(k), "%s %s label key %q", kind, name, k)
			require.Empty(t, validation.IsValidLabelValue(v), "%s %s label %s=%q", kind, name, k, v)
		}
	}

	for name, d := range m.deployments {
		checkMeta("Deployment", name, d.Labels)
		require.True(t, isSubset(d.Spec.Selector.MatchLabels, d.Spec.Template.Labels), "Deployment %s selects its pods", name)
	}
	for name, s := range m.statefulSets {
		checkMeta("StatefulSet", name, s.Labels)
		// Pods get a controller-revision-hash label of <name>-<hash>.
		require.LessOrEqual(t, len(name), 52, "StatefulSet name %q", name)
		require.True(t, isSubset(s.Spec.Selector.MatchLabels, s.Spec.Template.Labels), "StatefulSet %s selects its pods", name)
		require.Contains(t, m.services, s.Spec.ServiceName, "StatefulSet %s's governing Service", name)
	}
	for name, s := range m.services {
		checkMeta("Service", name, s.Labels)
		require.Empty(t, validation.IsDNS1035Label(name), "Service name %q", name)
		require.True(t, m.selects(s.Spec.Selector), "Service %s selects no pods", name)
	}
	for name, s := range m.secrets {
		checkMeta("Secret", name, s.Labels)
	}
	for name, i := range m.ingresses {
		checkMeta("Ingress", name, i.Labels)
		for _, rule := range i.Spec.Rules {
			for _, p := range rule.HTTP.Paths {
				svc, ok := m.services[p.Backend.Service.Name]
				require.True(t, ok, "Ingress %s routes to missing Service %s", name, p.Backend.Service.Name)
				require.True(t, slices.ContainsFunc(svc.Spec.Ports, func(sp corev1.ServicePort) bool {
					return sp.Name == p.Backend.Service.Port.Name
				}), "Ingress %s routes to a missing port", name)
			}
		}
	}
	for name, p := range m.policies {
		checkMeta("NetworkPolicy", name, p.Labels)
		require.True(t, m.selects(p.Spec.PodSelector.MatchLabels), "NetworkPolicy %s applies to no pods", name)
		for _, rule := range p.Spec.Ingress {
			for _, peer := range rule.From {
				require.True(t, m.selects(peer.PodSelector.MatchLabels), "NetworkPolicy %s admits no pods", name)
			}
		}
	}

	for _, w := range m.workloads() {
		checkMeta(w.name, strings.Fields(w.name)[1], w.labels)
		if sa := w.spec.ServiceAccountName; sa != "" && len(m.accounts) > 0 {
			require.Contains(t, m.accounts, sa, "%s's service account", w.name)
		}
		volumes := map[string]bool{}
		for _, v := range w.spec.Volumes {
			volumes[v.Name] = true
		}
		for _, c := range w.claims {
			volumes[c] = true
		}
		for _, c := range slices.Concat(w.spec.InitContainers, w.spec.Containers) {
			where := w.name + " container " + c.Name
			require.Empty(t, validation.IsDNS1123Label(c.Name), where)
			ports := map[string]bool{}
			for _, p := range c.Ports {
				ports[p.Name] = true
			}
			defined := map[string]bool{}
			for _, e := range c.Env {
				require.Empty(t, validation.IsEnvVarName(e.Name), "%s env %s", where, e.Name)
				for _, ref := range envRef.FindAllStringSubmatch(e.Value, -1) {
					require.True(t, defined[ref[1]], "%s: %s uses $(%s) before it is defined", where, e.Name, ref[1])
				}
				defined[e.Name] = true
				if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
					continue
				}
				ref := e.ValueFrom.SecretKeyRef
				if ref.Name == existingSecret {
					continue
				}
				secret, ok := m.secrets[ref.Name]
				require.True(t, ok, "%s: %s reads missing Secret %s", where, e.Name, ref.Name)
				if ref.Optional == nil || !*ref.Optional {
					require.Contains(t, secret.Data, ref.Key, "%s: %s reads a missing key", where, e.Name)
				}
			}
			for _, vm := range c.VolumeMounts {
				require.True(t, volumes[vm.Name], "%s mounts missing volume %s", where, vm.Name)
			}
			for _, p := range []*corev1.Probe{c.LivenessProbe, c.ReadinessProbe, c.StartupProbe} {
				if p != nil && p.HTTPGet != nil && p.HTTPGet.Port.Type == intstr.String {
					require.True(t, ports[p.HTTPGet.Port.StrVal], "%s probes missing port %s", where, p.HTTPGet.Port.StrVal)
				}
			}
		}
	}
}

// Lookups used by the tests.

func (m manifests) server(t *testing.T) appsv1.Deployment {
	t.Helper()
	d, ok := m.deployments["ci-cisid-server"]
	require.True(t, ok, "server Deployment")
	return d
}

func (m manifests) agent(t *testing.T) appsv1.StatefulSet {
	t.Helper()
	s, ok := m.statefulSets["ci-cisid-agent"]
	require.True(t, ok, "agent StatefulSet")
	return s
}

func container(t *testing.T, spec corev1.PodSpec, name string) corev1.Container {
	t.Helper()
	for _, c := range slices.Concat(spec.InitContainers, spec.Containers) {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no container %q", name)
	return corev1.Container{}
}

// env returns a container's variable, or nil.
func env(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

func volume(spec corev1.PodSpec, name string) *corev1.Volume {
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == name {
			return &spec.Volumes[i]
		}
	}
	return nil
}
