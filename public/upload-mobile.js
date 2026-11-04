(function () {
  const token = location.pathname.split("/").filter(Boolean).pop();
  const drop = document.getElementById("drop");
  const input = document.getElementById("files");
  const list = document.getElementById("list");
  const post = (path, body) => fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body)
  });

  function row(file) {
    const el = document.createElement("div");
    el.className = "file";
    const head = document.createElement("div");
    head.className = "file-head";
    const name = document.createElement("b");
    name.textContent = file.name;
    const size = document.createElement("small");
    size.textContent = (file.size / 1048576).toFixed(1) + " MB";
    head.append(name, size);
    const bar = document.createElement("div");
    bar.className = "bar";
    const fill = document.createElement("span");
    bar.append(fill);
    const status = document.createElement("div");
    status.className = "status";
    const left = document.createElement("span");
    left.textContent = "Waiting";
    const right = document.createElement("strong");
    right.textContent = "0%";
    status.append(left, right);
    el.append(head, bar, status);
    list.prepend(el);
    return {
      el, fill, left, right,
      id: crypto.randomUUID ? crypto.randomUUID() : Date.now() + "-" + Math.random().toString(16).slice(2)
    };
  }

  async function upload(file) {
    const ui = row(file);
    const id = ui.id;
    ui.left.textContent = "Uploading";
    post("/api/public/upload/" + encodeURIComponent(token) + "/progress", {
      uploadId: id, name: file.name, size: file.size, progress: 0, status: "uploading"
    }).catch(() => {});
    try {
      const form = new FormData();
      form.append("file", file, file.name);
      form.append("uploadId", id);
      await new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open("POST", "/api/public/upload/" + encodeURIComponent(token));
        xhr.upload.onprogress = (event) => {
          if (!event.lengthComputable) return;
          const percent = Math.max(1, Math.min(99, Math.round(event.loaded / event.total * 100)));
          ui.fill.style.width = percent + "%";
          ui.right.textContent = percent + "%";
          if (percent % 10 < 1) {
            post("/api/public/upload/" + encodeURIComponent(token) + "/progress", {
              uploadId: id, name: file.name, size: file.size, progress: percent, status: "uploading"
            }).catch(() => {});
          }
        };
        xhr.onload = () => {
          let payload = {};
          try { payload = JSON.parse(xhr.responseText || "{}"); } catch {}
          if (xhr.status >= 200 && xhr.status < 300) resolve(payload);
          else reject(new Error(payload.error && payload.error.message || "Upload failed"));
        };
        xhr.onerror = () => reject(new Error("Network error"));
        xhr.send(form);
      });
      ui.fill.style.width = "100%";
      ui.right.textContent = "100%";
      ui.left.textContent = "Uploaded";
      ui.el.style.borderColor = "var(--brand)";
      post("/api/public/upload/" + encodeURIComponent(token) + "/progress", {
        uploadId: id, name: file.name, size: file.size, progress: 100, status: "done"
      }).catch(() => {});
    } catch (error) {
      ui.left.textContent = error.message || "Upload failed";
      ui.right.textContent = "—";
      ui.el.style.borderColor = "#dc2626";
      post("/api/public/upload/" + encodeURIComponent(token) + "/progress", {
        uploadId: id, name: file.name, size: file.size, progress: 0, status: "error"
      }).catch(() => {});
    }
  }

  async function handle(files) {
    for (const file of Array.from(files || [])) await upload(file);
    input.value = "";
  }

  drop.addEventListener("click", () => input.click());
  input.addEventListener("change", () => handle(input.files));
  ["dragenter", "dragover"].forEach((name) => drop.addEventListener(name, (event) => {
    event.preventDefault();
    drop.classList.add("drag");
  }));
  ["dragleave", "drop"].forEach((name) => drop.addEventListener(name, (event) => {
    event.preventDefault();
    drop.classList.remove("drag");
  }));
  drop.addEventListener("drop", (event) => handle(event.dataTransfer.files));
})();
