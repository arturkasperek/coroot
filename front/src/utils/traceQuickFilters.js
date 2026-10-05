const TRACE_FACETS = [
    { key: 'Source', label: 'Source' },
    { key: 'Namespace', label: 'Namespace' },
    { key: 'ServiceName', label: 'Application' },
    { key: 'ApiRoute', label: 'API Route' },
    { key: 'SpanName', label: 'Root span name' },
];

export function displayTraceSourceName(value) {
    switch (String(value)) {
        case 'agent':
            return 'eBPF (node-agent)';
        case 'otel':
            return 'OpenTelemetry';
        default:
            return String(value || '');
    }
}

function traceFacetLabel(key, value) {
    if (key === 'Namespace' && value === 'n/a') {
        return 'Not applicable';
    }
    if (key === 'Source') {
        return displayTraceSourceName(value);
    }
    return value;
}

export function buildTraceQuickFilters(facets = []) {
    const backend = new Map((facets || []).map((group) => [group.key, group]));
    return TRACE_FACETS.map((def) => {
        const values = (backend.get(def.key)?.values || [])
            .filter((facet) => facet && facet.value)
            .map((facet) => ({
                value: facet.value,
                label: traceFacetLabel(def.key, facet.value),
                count: facet.count || 0,
                color: '',
            }));
        return { key: def.key, label: def.label, values };
    }).filter((group) => group.values.length > 0);
}

export function toTraceQuickFilters(filters = []) {
    return (filters || [])
        .filter((filter) => filter && filter.field)
        .map((filter) => ({
            name: filter.field,
            op: filter.op,
            value: filter.value,
        }));
}
