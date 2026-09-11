const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function load() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/heatmapSelection.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

function event(overrides = {}) {
    return { key: 'Escape', code: 'Escape', target: { tagName: 'DIV' }, ...overrides };
}

test('Escape clears an active heatmap time range', async () => {
    const { shouldClearHeatmapSelection } = await load();

    assert.equal(
        shouldClearHeatmapSelection(event(), {
            selection: { x1: 100, x2: 200, y1: '', y2: '' },
        }),
        true,
    );
});

test('Escape does nothing when no range is selected', async () => {
    const { shouldClearHeatmapSelection } = await load();

    assert.equal(shouldClearHeatmapSelection(event(), { selection: {} }), false);
    assert.equal(shouldClearHeatmapSelection(event(), { selection: { x1: 0, x2: 0, y1: '', y2: '' } }), false);
});

test('other keys do not clear the heatmap selection', async () => {
    const { shouldClearHeatmapSelection } = await load();

    assert.equal(
        shouldClearHeatmapSelection(event({ key: 'Enter', code: 'Enter' }), {
            selection: { x1: 100, x2: 200 },
        }),
        false,
    );
});

test('Escape does not clear selection while typing in an input', async () => {
    const { shouldClearHeatmapSelection } = await load();

    assert.equal(
        shouldClearHeatmapSelection(event({ target: { tagName: 'INPUT' } }), {
            selection: { x1: 100, x2: 200 },
        }),
        false,
    );
});

test('Escape does not clear selection when a dialog is open', async () => {
    const { shouldClearHeatmapSelection } = await load();

    assert.equal(
        shouldClearHeatmapSelection(event(), {
            selection: { x1: 100, x2: 200 },
            overlayOpen: true,
        }),
        false,
    );
});
