// Copy-to-clipboard for [data-copy] buttons: copies the attribute value and
// briefly flashes the [data-copy-label] child. Works in insecure contexts too.

export async function copyFrom(btn: HTMLElement): Promise<void> {
    const text = btn.getAttribute("data-copy") ?? "";
    const label = btn.querySelector<HTMLElement>("[data-copy-label]");
    const ok = await copyText(text);
    if (!label) return;
    const orig = label.textContent;
    label.textContent = ok ? "✓ copied" : "⚠ failed";
    setTimeout(() => {
        label.textContent = orig;
    }, 1400);
}

async function copyText(text: string): Promise<boolean> {
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
