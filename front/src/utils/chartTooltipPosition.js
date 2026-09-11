const DEFAULT_GAP = 5;

export function tooltipTranslate({
    cursorX,
    cursorY,
    tooltipWidth,
    tooltipHeight,
    viewWidth,
    viewHeight,
    preferLeft,
    gap = DEFAULT_GAP,
}) {
    let x = preferLeft ? cursorX - tooltipWidth - gap : cursorX + gap;
    let y = cursorY;
    x = Math.min(Math.max(0, x), Math.max(0, viewWidth - tooltipWidth));
    y = Math.min(Math.max(0, y), Math.max(0, viewHeight - tooltipHeight));
    return { x, y };
}
