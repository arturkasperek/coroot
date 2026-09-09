const TRACE_FACETS = [
    { key: 'ServiceName', label: 'Root service name' },
    { key: 'SpanName', label: 'Root span name' },
];

export function buildTraceQuickFilters(facets = []) {
    const backend = new Map((facets || []).map((group) => [group.key, group]));
    return TRACE_FACETS.map((def) => {
        const values = (backend.get(def.key)?.values || [])
            .filter((facet) => facet && facet.value)
            .map((facet) => ({
                value: facet.value,
                label: facet.value,
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
