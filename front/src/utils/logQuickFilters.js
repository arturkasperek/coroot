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

    const useBackendFacets = Array.isArray(options.facets);
    const backend = new Map((options.facets || []).map((g) => [g.key, g]));

    return defs
        .filter((d) => !hidden.has(d.key))
        .map((d) => {
            if (useBackendFacets && CORE_FACETS.some((c) => c.key === d.key)) {
                const group = backend.get(d.key);
                const values = (group?.values || []).map((facet) => ({
                    value: facet.value,
                    label: d.key === 'service.name' ? displayServiceName(facet.value) : facet.value,
                    count: facet.count || 0,
                    color: d.key === 'Severity' ? (options.severityFacets || []).find((s) => s.value === facet.value)?.color || '' : '',
                }));
                return { key: d.key, label: d.label, values };
            }

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

export function buildStableLogQuickFilters(rawGroups, filters, catalog = {}, options = {}) {
    const currentGroups = new Map((rawGroups || []).map((group) => [group.key, group]));
    const hidden = new Set(options.hiddenAttributes || []);
    const labels = {
        Severity: 'Severity',
        Cluster: 'Cluster',
        'service.name': 'Application',
        'host.name': 'Host',
    };
    const columns = options.columns || [];
    const activeGroups = new Map();

    for (const filter of filters || []) {
        if (!filter || hidden.has(filter.name) || !['=', '!='].includes(filter.op)) {
            continue;
        }
        const column = columns.find((item) => item && item.key === filter.name);
        const label = labels[filter.name] || column?.label || (column && filter.name);
        if (!label) {
            continue;
        }
        if (!activeGroups.has(filter.name)) {
            activeGroups.set(filter.name, { key: filter.name, label, values: [] });
        }
        const values = activeGroups.get(filter.name).values;
        const value = String(filter.value);
        if (!values.some((known) => known.value === value)) {
            values.push({
                value,
                label: filter.name === 'service.name' ? displayServiceName(value) : value,
                color: '',
            });
        }
    }

    const keys = [...Object.keys(catalog), ...(rawGroups || []).map((group) => group.key), ...activeGroups.keys()].filter(
        (key, index, all) => all.indexOf(key) === index,
    );

    return keys.map((key) => {
        const currentGroup = currentGroups.get(key);
        const catalogGroup = catalog[key] || currentGroup || activeGroups.get(key);
        const currentValues = new Map((currentGroup?.values || []).map((value) => [value.value, value]));
        const values = [...catalogGroup.values];
        for (const value of [...(currentGroup?.values || []), ...(activeGroups.get(key)?.values || [])]) {
            if (!values.some((known) => known.value === value.value)) {
                values.push(value);
            }
        }
        return {
            key,
            label: catalogGroup.label,
            values: values.map((value) => ({
                ...value,
                ...(currentValues.get(value.value) || {}),
                count: currentValues.get(value.value)?.count || 0,
            })),
        };
    });
}

export function isLogFacetActive(filters, name, op, value) {
    return (filters || []).some((filter) => filter.name === name && filter.op === op && String(filter.value) === String(value));
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
