const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function loadLogQuickFilters() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/logQuickFilters.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

test('restores active include and exclude controls from deserialized filters', async () => {
    const { isLogFacetActive } = await loadLogQuickFilters();
    const filters = [
        { name: 'Severity', op: '=', value: 'info' },
        { name: 'Severity', op: '!=', value: 'warning' },
    ];

    assert.equal(isLogFacetActive(filters, 'Severity', '=', 'info'), true);
    assert.equal(isLogFacetActive(filters, 'Severity', '!=', 'warning'), true);
    assert.equal(isLogFacetActive(filters, 'Severity', '=', 'warning'), false);
});

test('keeps an active excluded value visible when it is absent from filtered entries', async () => {
    const { buildStableLogQuickFilters, isLogFacetActive } = await loadLogQuickFilters();
    const rawGroups = [
        {
            key: 'service.name',
            label: 'Application',
            values: [{ value: '/k8s/coroot-dev/coroot', label: 'coroot-dev/coroot', count: 4, color: '' }],
        },
    ];
    const filters = [{ name: 'service.name', op: '!=', value: 'express-demo' }];

    const groups = buildStableLogQuickFilters(rawGroups, filters);
    const applications = groups.find((group) => group.key === 'service.name');
    const excluded = applications.values.find((value) => value.value === 'express-demo');

    assert.deepEqual(excluded, {
        value: 'express-demo',
        label: 'express-demo',
        color: '',
        count: 0,
    });
    assert.equal(isLogFacetActive(filters, 'service.name', '!=', excluded.value), true);
});

const backendFacets = [
    {
        key: 'Severity',
        values: [
            { value: 'unknown', count: 0 },
            { value: 'info', count: 38 },
            { value: 'warning', count: 0 },
            { value: 'error', count: 12 },
        ],
    },
    {
        key: 'Namespace',
        values: [{ value: 'coroot-dev', count: 12 }],
    },
    {
        key: 'Application',
        values: [
            { value: 'express-demo', count: 10 },
            { value: 'nextjs-demo', count: 2 },
        ],
    },
    {
        key: 'host.name',
        values: [
            { value: 'node-a', count: 10 },
            { value: 'node-b', count: 2 },
        ],
    },
    { key: 'Cluster', values: [{ value: 'default', count: 12 }] },
];

const pageEntries = Array.from({ length: 100 }, (_, i) => ({
    severity: 'error',
    cluster: 'default',
    attributes: {
        'service.name': i < 90 ? '/k8s/coroot-dev/express-demo' : '/k8s/coroot-dev/nextjs-demo',
        'host.name': i < 90 ? 'node-a' : 'node-b',
    },
}));

test('uses ClickHouse facet counts instead of the loaded page', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, { facets: backendFacets });
    const apps = groups.find((g) => g.key === 'Application');
    const sev = groups.find((g) => g.key === 'Severity');
    assert.equal(apps.values.find((v) => v.value === 'express-demo').count, 10);
    assert.equal(sev.values.find((v) => v.value === 'info').count, 38);
    assert.equal(sev.values.find((v) => v.value === 'error').count, 12);
});

test('falls back to counting entries when facets is omitted', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, {});
    const apps = groups.find((g) => g.key === 'Application');
    assert.equal(apps.values.find((v) => v.value === 'express-demo').count, 90);
});

const facetValues = [
    { value: '/k8s/coroot-dev/express-demo', label: 'coroot-dev/express-demo', count: 10 },
    { value: '/k8s/coroot-dev/nextjs-demo', label: 'coroot-dev/nextjs-demo', count: 2 },
    { value: 'ssh.service', label: 'ssh.service', count: 848 },
    { value: 'ollama', label: 'ollama', count: 26 },
];

test('group search is available for Namespace and Application', async () => {
    const { groupHasLocalSearch } = await loadLogQuickFilters();
    assert.equal(groupHasLocalSearch('Namespace'), true);
    assert.equal(groupHasLocalSearch('Application'), true);
    assert.equal(groupHasLocalSearch('host.name'), true);
    assert.equal(groupHasLocalSearch('service.name'), false);
    assert.equal(groupHasLocalSearch('Severity'), false);
    assert.equal(groupHasLocalSearch('Cluster'), false);
});

test('empty group search keeps all facet values', async () => {
    const { filterFacetValues } = await loadLogQuickFilters();
    assert.equal(filterFacetValues(facetValues, '').length, 4);
    assert.equal(filterFacetValues(facetValues, '   ').length, 4);
});

test('group search matches display label case-insensitively', async () => {
    const { filterFacetValues } = await loadLogQuickFilters();
    const matched = filterFacetValues(facetValues, 'Express');
    assert.deepEqual(
        matched.map((v) => v.label),
        ['coroot-dev/express-demo'],
    );
});

test('group search matches raw facet value when the label is shortened', async () => {
    const { filterFacetValues } = await loadLogQuickFilters();
    const matched = filterFacetValues(facetValues, 'k8s');
    assert.deepEqual(
        matched.map((v) => v.value),
        ['/k8s/coroot-dev/express-demo', '/k8s/coroot-dev/nextjs-demo'],
    );
});

test('does not mix backend severity with client-side application when facets is present', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, {
        facets: [
            {
                key: 'Severity',
                values: [
                    { value: 'info', count: 38 },
                    { value: 'error', count: 12 },
                    { value: 'unknown', count: 0 },
                    { value: 'warning', count: 0 },
                ],
            },
        ],
    });
    const apps = groups.find((g) => g.key === 'Application');
    assert.equal(apps, undefined);
});

test('displays Source facet values as Container logs and OpenTelemetry', async () => {
    const { displaySourceName } = await loadLogQuickFilters();
    assert.equal(displaySourceName('agent'), 'Container logs');
    assert.equal(displaySourceName('otel'), 'OpenTelemetry');
});

test('formats Source query chips with display labels', async () => {
    const { formatLogFilter } = await loadLogQuickFilters();
    assert.equal(formatLogFilter({ name: 'Source', op: '=', value: 'agent' }), 'Source = Container logs');
    assert.equal(formatLogFilter({ name: 'Source', op: '!=', value: 'otel' }), 'Source != OpenTelemetry');
    assert.equal(formatLogFilter({ name: 'Severity', op: '=', value: 'error' }), 'Severity = error');
    assert.equal(formatLogFilter({ name: 'Message', op: 'contains', value: 'boom' }), 'Message 🔍 boom');
});

test('puts Source before Severity in filter groups', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, {
        facets: [
            ...backendFacets,
            {
                key: 'Source',
                values: [
                    { value: 'agent', count: 90 },
                    { value: 'otel', count: 10 },
                ],
            },
        ],
    });
    assert.deepEqual(
        groups.map((g) => g.key),
        ['Source', 'Severity', 'Cluster', 'Namespace', 'Application', 'host.name'],
    );
});

test('keeps Source first after catalog merge', async () => {
    const { buildStableLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildStableLogQuickFilters(
        [
            { key: 'Severity', label: 'Severity', values: [{ value: 'info', label: 'info', count: 1, color: '' }] },
            { key: 'Source', label: 'Source', values: [{ value: 'agent', label: 'Container logs', count: 1, color: '' }] },
        ],
        [],
        {
            Severity: { key: 'Severity', label: 'Severity', values: [{ value: 'info', label: 'info', color: '' }] },
        },
    );
    assert.equal(groups[0].key, 'Source');
    assert.equal(groups[1].key, 'Severity');
});

test('uses backend Source facet counts with display labels', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, {
        facets: [
            ...backendFacets,
            {
                key: 'Source',
                values: [
                    { value: 'agent', count: 90 },
                    { value: 'otel', count: 10 },
                ],
            },
        ],
    });
    const source = groups.find((g) => g.key === 'Source');
    assert.deepEqual(
        source.values.map((v) => [v.value, v.label, v.count]),
        [
            ['agent', 'Container logs', 90],
            ['otel', 'OpenTelemetry', 10],
        ],
    );
});

test('falls back to deriving Source from service.name', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(
        [
            { attributes: { 'service.name': '/k8s/coroot-dev/express-demo' } },
            { attributes: { 'service.name': '/k8s/coroot-dev/express-demo' } },
            { attributes: { 'service.name': 'checkout' } },
        ],
        {},
    );
    const source = groups.find((g) => g.key === 'Source');
    assert.equal(source.values.find((v) => v.value === 'agent').count, 2);
    assert.equal(source.values.find((v) => v.value === 'otel').count, 1);
});

test('keeps an active Source filter visible', async () => {
    const { buildStableLogQuickFilters, isLogFacetActive } = await loadLogQuickFilters();
    const groups = buildStableLogQuickFilters([], [{ name: 'Source', op: '=', value: 'agent' }]);
    const source = groups.find((g) => g.key === 'Source');
    assert.equal(source.label, 'Source');
    assert.deepEqual(source.values[0], { value: 'agent', label: 'Container logs', color: '', count: 0 });
    assert.equal(isLogFacetActive([{ name: 'Source', op: '=', value: 'agent' }], 'Source', '=', 'agent'), true);
});

test('displays Namespace n/a as Not applicable', async () => {
    const { displayNamespaceName, formatLogFilter } = await loadLogQuickFilters();
    assert.equal(displayNamespaceName('n/a'), 'Not applicable');
    assert.equal(displayNamespaceName('coroot-dev'), 'coroot-dev');
    assert.equal(formatLogFilter({ name: 'Namespace', op: '=', value: 'n/a' }), 'Namespace = Not applicable');
});

test('Application facet uses short names from backend', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters([], {
        facets: [
            { key: 'Application', values: [{ value: 'express-demo', count: 40 }] },
            {
                key: 'Namespace',
                values: [
                    { value: 'coroot-dev', count: 32 },
                    { value: 'n/a', count: 0 },
                ],
            },
        ],
    });
    assert.deepEqual(
        groups.map((g) => g.key),
        ['Source', 'Severity', 'Cluster', 'Namespace', 'Application', 'Host'].filter((k) => groups.some((g) => g.key === k)),
    );
    const apps = groups.find((g) => g.key === 'Application');
    assert.equal(apps.values[0].value, 'express-demo');
    assert.equal(apps.values[0].label, 'express-demo');
    const ns = groups.find((g) => g.key === 'Namespace');
    assert.equal(ns.values.find((v) => v.value === 'n/a').label, 'Not applicable');
});

test('falls back to deriving Namespace and Application from attributes', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(
        [
            { attributes: { 'service.name': '/k8s/coroot-dev/express-demo' } },
            { attributes: { 'service.name': 'checkout', 'k8s.namespace.name': 'coroot-dev' } },
            { attributes: { 'service.name': 'ollama' } },
        ],
        {},
    );
    const ns = groups.find((g) => g.key === 'Namespace');
    assert.equal(ns.values.find((v) => v.value === 'coroot-dev').count, 2);
    assert.equal(ns.values.find((v) => v.value === 'n/a').count, 1);
    const apps = groups.find((g) => g.key === 'Application');
    assert.equal(apps.values.find((v) => v.value === 'express-demo').count, 1);
    assert.equal(apps.values.find((v) => v.value === 'checkout').count, 1);
});

test('parses free text as a Message contains filter', async () => {
    const { messageFilterFromFreeText } = await loadLogQuickFilters();
    assert.deepEqual(messageFilterFromFreeText('some text'), {
        name: 'Message',
        op: 'contains',
        value: 'some text',
    });
    assert.deepEqual(messageFilterFromFreeText('  timeout  '), {
        name: 'Message',
        op: 'contains',
        value: 'timeout',
    });
});

test('parses leading exclamation as Message not contains', async () => {
    const { messageFilterFromFreeText, formatLogFilter } = await loadLogQuickFilters();
    assert.deepEqual(messageFilterFromFreeText('!some text'), {
        name: 'Message',
        op: 'not contains',
        value: 'some text',
    });
    assert.deepEqual(messageFilterFromFreeText('!  boom'), {
        name: 'Message',
        op: 'not contains',
        value: 'boom',
    });
    assert.equal(formatLogFilter(messageFilterFromFreeText('!boom')), 'Message !🔍 boom');
});

test('ignores empty or bang-only free text', async () => {
    const { messageFilterFromFreeText } = await loadLogQuickFilters();
    assert.equal(messageFilterFromFreeText(''), null);
    assert.equal(messageFilterFromFreeText('   '), null);
    assert.equal(messageFilterFromFreeText('!'), null);
    assert.equal(messageFilterFromFreeText('!   '), null);
    assert.equal(messageFilterFromFreeText(null), null);
});

test('Enter prefers a matched suggestion over Message search', async () => {
    const { resolveQueryBuilderEnter } = await loadLogQuickFilters();
    assert.deepEqual(resolveQueryBuilderEnter({ mode: 'name', str: 'app', matchedItem: 'Application' }), { action: 'select', value: 'Application' });
});

test('Enter on unmatched name text adds a Message filter', async () => {
    const { resolveQueryBuilderEnter } = await loadLogQuickFilters();
    assert.deepEqual(resolveQueryBuilderEnter({ mode: 'name', str: 'some text', matchedItem: undefined }), {
        action: 'push-filter',
        filter: { name: 'Message', op: 'contains', value: 'some text' },
    });
    assert.deepEqual(resolveQueryBuilderEnter({ mode: 'name', str: '!error', matchedItem: undefined }), {
        action: 'push-filter',
        filter: { name: 'Message', op: 'not contains', value: 'error' },
    });
});

test('Enter does not create a Message filter when free text is disabled', async () => {
    const { resolveQueryBuilderEnter } = await loadLogQuickFilters();
    assert.deepEqual(resolveQueryBuilderEnter({ mode: 'name', str: 'express-demo', matchedItem: undefined, allowFreeText: false }), {
        action: 'none',
    });
});

test('Enter in value mode still uses a custom value', async () => {
    const { resolveQueryBuilderEnter } = await loadLogQuickFilters();
    assert.deepEqual(resolveQueryBuilderEnter({ mode: 'value', str: 'some text', matchedItem: undefined }), { action: 'select', value: 'some text' });
    assert.deepEqual(resolveQueryBuilderEnter({ mode: 'name', str: '', matchedItem: undefined }), { action: 'none' });
});
