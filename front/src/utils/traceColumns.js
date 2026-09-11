const HTTP_METHODS = 'GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|CONNECT|TRACE';
const METHOD_AND_PATH = new RegExp(`^(?:${HTTP_METHODS})\\s+(.+)$`, 'i');

export const TRACE_NAME_SAME_MARK = '=';

function stripQuery(path) {
    const s = String(path || '');
    const i = s.indexOf('?');
    return (i >= 0 ? s.slice(0, i) : s).trim();
}

function pathnameFromUrl(url) {
    const s = String(url || '');
    try {
        return new URL(s).pathname || '';
    } catch {
        const withoutHost = s.replace(/^[a-z][a-z0-9+.-]*:\/\/[^/]+/i, '');
        return stripQuery(withoutHost) || '';
    }
}

function httpMethod(attributes) {
    return String((attributes && (attributes['http.method'] || attributes['http.request.method'])) || '').trim();
}

function pathOnly(value) {
    const s = String(value || '');
    const match = s.match(METHOD_AND_PATH);
    return match ? match[1] : s;
}

function withMethod(method, path) {
    if (!path) {
        return '';
    }
    return method ? `${method} ${path}` : path;
}

export function traceApiRoute(attributes) {
    if (!attributes) {
        return '';
    }
    const method = httpMethod(attributes);
    if (attributes['http.route']) {
        return withMethod(method, stripQuery(attributes['http.route']));
    }
    if (attributes['http.target']) {
        return withMethod(method, stripQuery(attributes['http.target']));
    }
    if (attributes['url.path']) {
        return withMethod(method, stripQuery(attributes['url.path']));
    }
    if (attributes['http.url']) {
        return withMethod(method, pathnameFromUrl(attributes['http.url']));
    }
    return '';
}

export function traceDisplayName(name, apiRoute) {
    const n = String(name || '');
    const r = String(apiRoute || '');
    if (!n) {
        return '';
    }
    if (!r) {
        return n;
    }
    if (n === r) {
        return TRACE_NAME_SAME_MARK;
    }
    const namePath = pathOnly(n);
    const routePath = pathOnly(r);
    if (namePath && namePath === routePath) {
        return TRACE_NAME_SAME_MARK;
    }
    return n;
}

export function traceColumnValue(item, header) {
    const key = (header && (header.value || header.key)) || '';
    if (key === 'api_route') {
        return traceApiRoute(item && item.attributes) || '—';
    }
    if (key === 'name') {
        const route = traceApiRoute(item && item.attributes);
        return traceDisplayName(item && item.name, route);
    }
    return item ? item[key] : undefined;
}
