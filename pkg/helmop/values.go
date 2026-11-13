package helmop

func tlsPaths() map[string]any {
	return map[string]any{
		"caCert": CertDir + "/ca.crt",
		"cert":   CertDir + "/tls.crt",
		"key":    CertDir + "/tls.key",
	}
}

func mtlsVolume(secretName string) map[string]any {
	return map[string]any{
		"name": "mtls",
		"secret": map[string]any{
			"secretName": secretName,
		},
	}
}

func mtlsVolumeMount() map[string]any {
	return map[string]any{
		"name":      "mtls",
		"mountPath": CertDir,
		"readOnly":  true,
	}
}

func volumes(secretName string, base ...map[string]any) []any {
	result := make([]any, 0, len(base)+1)
	for _, volume := range base {
		result = append(result, volume)
	}
	return append(result, mtlsVolume(secretName))
}

func volumeMounts(base ...map[string]any) []any {
	result := make([]any, 0, len(base)+1)
	for _, mount := range base {
		result = append(result, mount)
	}
	return append(result, mtlsVolumeMount())
}

func logVolume() map[string]any {
	return map[string]any{"name": "logs", "emptyDir": map[string]any{}}
}

func logVolumeMount(path string) map[string]any {
	return map[string]any{"name": "logs", "mountPath": path}
}
