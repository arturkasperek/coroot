export const SORT_FIELD = 'Sort';
export const TRACE_SORT_VALUES = ['Date desc', 'Date asc', 'Duration desc', 'Duration asc'];
export const LOG_SORT_VALUES = ['Date desc', 'Date asc'];

export function effectiveSort(sort) {
    if (sort && sort.by) {
        return { by: sort.by, dir: sort.dir === 'asc' ? 'asc' : 'desc' };
    }
    return { by: 'date', dir: 'desc' };
}

export function toggleSort(sort, by) {
    const current = effectiveSort(sort);
    if (current.by === by) {
        return { by, dir: current.dir === 'desc' ? 'asc' : 'desc' };
    }
    return { by, dir: 'desc' };
}

export function formatSortValue(sort) {
    const by = sort && sort.by === 'duration' ? 'Duration' : 'Date';
    const dir = sort && sort.dir === 'asc' ? 'asc' : 'desc';
    return `${by} ${dir}`;
}

export function parseSortValue(value) {
    const match = String(value || '')
        .trim()
        .match(/^(Date|Duration)\s+(asc|desc)$/i);
    if (!match) {
        return null;
    }
    return { by: match[1].toLowerCase(), dir: match[2].toLowerCase() };
}

export function appendSortChip(filters = [], sort) {
    const rest = (filters || []).filter((filter) => filter && filter.name !== SORT_FIELD);
    if (!sort || !sort.by) {
        return rest;
    }
    return [...rest, { name: SORT_FIELD, op: '=', value: formatSortValue(sort) }];
}

export function extractSort(filters = []) {
    let sort = null;
    const rest = [];
    for (const filter of filters || []) {
        if (filter && filter.name === SORT_FIELD) {
            sort = parseSortValue(filter.value);
            continue;
        }
        rest.push(filter);
    }
    return { filters: rest, sort };
}

export function applyBuilderFilters(query, filters) {
    const parsed = extractSort(filters);
    const sort = parsed.sort || undefined;
    if (sameFilters(query.filters, parsed.filters) && sameSort(query.sort, sort)) {
        return query;
    }
    return { ...query, filters: parsed.filters, sort };
}

function sameSort(a, b) {
    if (!a && !b) {
        return true;
    }
    if (!a || !b) {
        return false;
    }
    return a.by === b.by && (a.dir || 'desc') === (b.dir || 'desc');
}

function sameFilters(a, b) {
    return JSON.stringify(a || []) === JSON.stringify(b || []);
}

export function makeQueryFromRoute(raw) {
    let q = raw;
    if (typeof raw === 'string') {
        try {
            q = JSON.parse(raw || '{}');
        } catch {
            q = {};
        }
    }
    q = q || {};
    const query = {
        view: q.view || 'messages',
        filters: q.filters || [],
        limit: q.limit || 100,
    };
    if (q.sort && q.sort.by) {
        query.sort = q.sort;
    }
    return query;
}

export function shouldReloadQuery(prevSerialized, query) {
    const serialized = JSON.stringify(query);
    return { reload: serialized !== prevSerialized, serialized };
}
