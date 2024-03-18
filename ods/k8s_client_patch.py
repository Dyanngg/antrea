def get_antrea_controller_pod():
    k8sclient = K8sClient()
    pods = k8sclient.list_namespaced_pod(namespace='kube-system', selector="component=antrea-controller")
    if len(pods.items) == 0:
        return None
    return pods.items[0]
