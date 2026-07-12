// Copy-to-clipboard for [data-copy] buttons: copies the attribute value and
// briefly swaps the [data-copy-label] icon to a checkmark. Works in insecure
// contexts too.

// Clipboard + check icons, shared with the readme code-block copy button.
export const COPY_ICON = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>`;
export const CHECK_ICON = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>`;

export async function copyFrom(btn: HTMLElement): Promise<void> {
    const text = btn.getAttribute("data-copy") ?? "";
    const label = btn.querySelector<HTMLElement>("[data-copy-label]");
    const ok = await copyText(text);
    if (!label || !ok) return;
    const orig = label.innerHTML;
    label.innerHTML = CHECK_ICON;
    btn.classList.add("copied");
    setTimeout(() => {
        label.innerHTML = orig;
        btn.classList.remove("copied");
    }, 1400);
}

export async function copyText(text: string): Promise<boolean> {
    if (navigator.clipboard?.writeText) {
        try {
            await navigator.clipboard.writeText(text);
            return true;
        } catch {
            /* fall through to legacy path */
        }
    }
    return legacyCopy(text);
}

function legacyCopy(text: string): boolean {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
        ok = document.execCommand("copy");
    } catch {
        ok = false;
    }
    document.body.removeChild(ta);
    return ok;
}
