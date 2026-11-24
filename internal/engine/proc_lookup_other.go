//go:build !darwin

package engine

func lookupLoopbackClients(proxyPort int) map[string]string {
	return nil
}
