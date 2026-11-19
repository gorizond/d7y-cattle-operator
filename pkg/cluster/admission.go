package cluster

func setAdmissionCondition(status *statusBuilder, roles Roles, err error, ready bool, message string) {
	if !roles.Admission {
		status.set(conditionAdmissionWebhookReady, false, "Disabled", "admission webhook is not enabled")
		return
	}
	if err != nil {
		status.set(conditionAdmissionWebhookReady, false, "ApplyFailed", err.Error())
		return
	}
	if !ready {
		if message == "" {
			message = "admission webhook HelmOp is not Ready"
		}
		status.set(conditionAdmissionWebhookReady, false, "Progressing", message)
		return
	}
	status.set(conditionAdmissionWebhookReady, true, "Ready", "admission webhook HelmOp Ready")
}
