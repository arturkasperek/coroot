export function spanK8sNamespace(attributes) {
    return (attributes && attributes['k8s.namespace.name']) || '';
}
