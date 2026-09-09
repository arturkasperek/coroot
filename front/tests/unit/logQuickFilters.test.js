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
        key: 'service.name',
        values: [
            { value: '/k8s/coroot-dev/express-demo', count: 10 },
            { value: '/k8s/coroot-dev/nextjs-demo', count: 2 },
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
    const apps = groups.find((g) => g.key === 'service.name');
    const sev = groups.find((g) => g.key === 'Severity');
    assert.equal(apps.values.find((v) => v.value.includes('express-demo')).count, 10);
    assert.equal(sev.values.find((v) => v.value === 'info').count, 38);
    assert.equal(sev.values.find((v) => v.value === 'error').count, 12);
});

test('falls back to counting entries when facets is omitted', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, {});
    const apps = groups.find((g) => g.key === 'service.name');
    assert.equal(apps.values.find((v) => v.value.includes('express-demo')).count, 90);
});

const facetValues = [
    { value: '/k8s/coroot-dev/express-demo', label: 'coroot-dev/express-demo', count: 10 },
    { value: '/k8s/coroot-dev/nextjs-demo', label: 'coroot-dev/nextjs-demo', count: 2 },
    { value: 'ssh.service', label: 'ssh.service', count: 848 },
    { value: 'ollama', label: 'ollama', count: 26 },
];

test('group search is available for Application and Host only', async () => {
    const { groupHasLocalSearch } = await loadLogQuickFilters();
    assert.equal(groupHasLocalSearch('service.name'), true);
    assert.equal(groupHasLocalSearch('host.name'), true);
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
    const apps = groups.find((g) => g.key === 'service.name');
    assert.equal(apps, undefined);
});
