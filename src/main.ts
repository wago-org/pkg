// Entry point — boots the single-page registry app.
import { init } from "./app.js";

if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", () => void init());
} else {
    void init();
}
