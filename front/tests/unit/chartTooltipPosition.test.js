const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function load() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/chartTooltipPosition.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

test('places the tooltip to the right of the cursor on the left half of the chart', async () => {
    const { tooltipTranslate } = await load();

    assert.deepEqual(
        tooltipTranslate({
            cursorX: 140,
            cursorY: 230,
            tooltipWidth: 120,
            tooltipHeight: 80,
            viewWidth: 800,
            viewHeight: 600,
            preferLeft: false,
        }),
        { x: 145, y: 230 },
    );
});

test('places the tooltip to the left of the cursor on the right half of the chart', async () => {
    const { tooltipTranslate } = await load();

    assert.deepEqual(
        tooltipTranslate({
            cursorX: 500,
            cursorY: 230,
            tooltipWidth: 120,
            tooltipHeight: 80,
            viewWidth: 800,
            viewHeight: 600,
            preferLeft: true,
        }),
        { x: 375, y: 230 },
    );
});

test('lets the tooltip overflow the chart and only clamps to the viewport', async () => {
    const { tooltipTranslate } = await load();

    assert.deepEqual(
        tooltipTranslate({
            cursorX: 200,
            cursorY: 420,
            tooltipWidth: 120,
            tooltipHeight: 180,
            viewWidth: 800,
            viewHeight: 600,
            preferLeft: false,
        }),
        { x: 205, y: 420 },
    );
});

test('shifts the tooltip up when it would overflow the viewport bottom', async () => {
    const { tooltipTranslate } = await load();

    assert.deepEqual(
        tooltipTranslate({
            cursorX: 200,
            cursorY: 550,
            tooltipWidth: 120,
            tooltipHeight: 180,
            viewWidth: 800,
            viewHeight: 600,
            preferLeft: false,
        }),
        { x: 205, y: 420 },
    );
});

test('clamps a wide tooltip to the viewport instead of the chart', async () => {
    const { tooltipTranslate } = await load();

    assert.deepEqual(
        tooltipTranslate({
            cursorX: 20,
            cursorY: 100,
            tooltipWidth: 380,
            tooltipHeight: 40,
            viewWidth: 400,
            viewHeight: 600,
            preferLeft: false,
        }),
        { x: 20, y: 100 },
    );
});
