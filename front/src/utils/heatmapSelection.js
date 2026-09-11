export function heatmapHasSelection(selection) {
    if (!selection) {
        return false;
    }
    return Boolean(selection.x1 || selection.x2 || selection.y1 || selection.y2);
}

export function shouldClearHeatmapSelection(event, { selection, overlayOpen = false } = {}) {
    if (!heatmapHasSelection(selection)) {
        return false;
    }
    if (event.key !== 'Escape' && event.code !== 'Escape') {
        return false;
    }
    if (overlayOpen) {
        return false;
    }
    const target = event.target;
    if (target) {
        const tag = target.tagName;
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable) {
            return false;
        }
    }
    return true;
}
