// The HTML entry point is always revalidated. Force a service-worker update
// check here as well, so a browser still running a previous cached app.js
// cannot pin the old shell for another day.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.getRegistration()
    .then((registration) => registration?.update())
    .catch(() => {});
}
