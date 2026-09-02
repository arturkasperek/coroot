const CORE_FACETS = [
    { key: 'Severity', label: 'Severity', from: 'severity' },
    { key: 'Cluster', label: 'Cluster', from: 'cluster' },
    { key: 'service.name', label: 'Application', from: 'attr:service.name' },
    { key: 'host.name', label: 'Host', from: 'attr:host.name' },
];

const SKIP_COLUMN_KEYS = new Set(['date', 'message', 'application', 'cluster']);

export function displayServiceName(value) {
    if (!value) {
        return '';
    }
    const s = String(value);
    if (s.startsWith('/')) {
        const parts = s.split('/').filter(Boolean);
        return parts.length > 1 ? parts.slice(1).join('/') : s;
    }
    return s;
}

export function facetValue(entry, from) {
    if (!from) {
        return '';
    }
    if (from.startsWith('attr:')) {
        const name = from.slice(5);
        return (entry.attributes && entry.attributes[name]) || '';
    }
    return entry[from] || '';
}

export function buildLogQuickFilters(entries, options = {}) {
    const hidden = new Set(options.hiddenAttributes || []);
    const columns = options.columns || [];

    const defs = [...CORE_FACETS];
    for (const col of columns) {
        if (!col || !col.key || SKIP_COLUMN_KEYS.has(col.key) || hidden.has(col.key)) {
            continue;
        }
        if (defs.some((d) => d.key === col.key)) {
            continue;
        }
        defs.push({ key: col.key, label: col.label || col.key, from: `attr:${col.key}` });
    }

    return defs
        .filter((d) => !hidden.has(d.key))
        .map((d) => {
            if (d.key === 'Severity' && options.severityFacets && options.severityFacets.length) {
                return {
                    key: d.key,
                    label: d.label,
                    values: options.severityFacets.map((facet) => ({
                        value: facet.value,
                        label: facet.label || facet.value,
                        count: facet.count || 0,
                        color: facet.color || '',
                    })),
                };
            }

            const counts = new Map();
            const colors = new Map();
            for (const e of entries || []) {
                const value = String(facetValue(e, d.from) || '').trim();
                if (!value) {
                    continue;
                }
                counts.set(value, (counts.get(value) || 0) + 1);
                if (d.key === 'Severity' && e.color && !colors.has(value)) {
                    colors.set(value, e.color);
                }
            }
            const values = [...counts.entries()]
                .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
                .map(([value, count]) => ({
                    value,
                    count,
                    label: d.key === 'service.name' ? displayServiceName(value) : value,
                    color: colors.get(value) || '',
                }));
            return { key: d.key, label: d.label, values };
        })
        .filter((g) => g.values.length > 0);
}

export function formatCount(n) {
    const v = Number(n) || 0;
    if (v < 1000) {
        return String(v);
    }
    const units = [
        { div: 1e9, suffix: 'B' },
        { div: 1e6, suffix: 'M' },
        { div: 1e3, suffix: 'K' },
    ];
    for (const u of units) {
        if (v >= u.div) {
            const scaled = v / u.div;
            const digits = scaled >= 100 ? 0 : 1;
            return scaled.toFixed(digits).replace(/\.0$/, '') + u.suffix;
        }
    }
    return String(v);
}

export function isFacetChecked(filters, name, value) {
    const list = filters || [];
    const equals = list.filter((f) => f.name === name && f.op === '=');
    if (equals.length) {
        return equals.some((f) => f.value === value);
    }
    return !list.some((f) => f.name === name && f.op === '!=' && f.value === value);
}
