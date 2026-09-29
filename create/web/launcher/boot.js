// boot.js is a plain script, older than the page's modules on purpose: when a
// browser cannot run app.js (an old phone, a blocked file), it says so on the
// page with the browser's own error, instead of leaving empty steps.
(function () {
  var shown = false;
  function show(msg) {
    if (shown) return;
    shown = true;
    var box = document.createElement("div");
    box.className = "broken";
    box.setAttribute("role", "alert");
    box.textContent = "Esta página não carregou por inteiro neste navegador. Atualize o navegador (ou use o Chrome) e recarregue. Detalhe para o suporte: " + msg;
    var top = document.querySelector(".top");
    if (top && top.parentNode) top.parentNode.insertBefore(box, top.nextSibling);
    else document.body.insertBefore(box, document.body.firstChild);
  }
  window.addEventListener("error", function (e) {
    if (!window.launcherReady) show((e && e.message) || "erro ao carregar o script");
  });
  window.addEventListener("load", function () {
    setTimeout(function () {
      if (!window.launcherReady) show("o script principal não rodou (" + navigator.userAgent + ")");
    }, 4000);
  });
})();
