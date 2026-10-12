/* Small native-control helpers; feature scripts retain all business behavior. */
(function (root) {
  "use strict";
  function createButton(doc, options) {
    options = options || {};
    var variant = options.variant || "secondary";
    var size = options.size || "regular";
    if (["primary", "secondary", "danger"].indexOf(variant) < 0) throw new TypeError("invalid button variant");
    if (["regular", "compact", "icon"].indexOf(size) < 0) throw new TypeError("invalid button size");
    if (size === "icon" && !options.label) throw new TypeError("icon-only buttons require a label");
    var type = options.type || "button";
    if (["button", "submit", "reset"].indexOf(type) < 0) throw new TypeError("invalid button type");
    var button = doc.createElement("button");
    button.type = type;
    button.className = "ui-button ui-button--" + variant + (size === "regular" ? "" : " ui-button--" + size);
    button.textContent = options.text || "";
    if (options.label) button.setAttribute("aria-label", options.label);
    if (options.disabled) button.disabled = true;
    return button;
  }
  function openDialog(dialog, trigger) {
    if (!dialog || typeof dialog.showModal !== "function") throw new TypeError("native dialog required");
    var prior = trigger || dialog.ownerDocument.activeElement;
    var closed = false;
    function restoreFocus() {
      if (closed) return;
      closed = true;
      dialog.removeEventListener("close", restoreFocus);
      if (prior && typeof prior.focus === "function" && prior.isConnected !== false) prior.focus();
    }
    dialog.addEventListener("close", restoreFocus);
    try {
      dialog.showModal();
    } catch (error) {
      dialog.removeEventListener("close", restoreFocus);
      throw error;
    }
    return function close() {
      if (dialog.open) dialog.close();
      else restoreFocus();
    };
  }
  function enhanceDisclosure(details) {
    if (!details || details.tagName !== "DETAILS") throw new TypeError("details element required");
    var summary = details.querySelector("summary");
    if (!summary) throw new TypeError("details summary required");
    function sync() { summary.setAttribute("aria-expanded", details.open ? "true" : "false"); }
    details.addEventListener("toggle", sync);
    sync();
    return function destroy() { details.removeEventListener("toggle", sync); };
  }
  var api = { createButton: createButton, openDialog: openDialog, enhanceDisclosure: enhanceDisclosure };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  root.HMDComponents = api;
})(typeof globalThis !== "undefined" ? globalThis : this);
