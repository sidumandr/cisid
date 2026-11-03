// Package charttest checks the cisid Helm chart without a cluster: it
// renders the chart with the Helm SDK, decodes every object strictly into
// its Kubernetes API type and checks the references between them.
//
// It is a module of its own so the main module does not depend on Helm.
// Run it from this directory with "go test ./...".
package charttest
