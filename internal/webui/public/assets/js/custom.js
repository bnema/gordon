function initializeVersionCheck() {
  const updateStrTemplate = "New version %VERSION% available, consider pulling the latest image";

  const checkVersion = (versionData) => {
    const currentVersion = document.getElementById("actual-version").textContent.trim();
    const fetchedVersionNumber = versionData?.amd64?.name.match(/\d+\.\d+\.\d+/)?.[0];

    if (!fetchedVersionNumber) {
      console.log("Dev mode detected, skipping version check.");
      return;
    }

    if (currentVersion !== fetchedVersionNumber) {
      const updateStr = updateStrTemplate.replace("%VERSION%", fetchedVersionNumber);
      console.log(updateStr);
      const updateElement = document.getElementById("update-available");
      updateElement.title = updateStr;
      updateElement.removeAttribute("hidden");
      updateElement.classList.remove("hidden"); // Remove the Tailwind `hidden` class
      updateElement.classList.add("block"); // Add the Tailwind `block` class
    } else {
      console.log("No new version. Current version is up-to-date:", currentVersion);
    }
  };

  const fetchVersionInfo = () => {
    fetch("https://gordon-proxy.bnema.dev/version")
      .then((response) => response.json())
      .then(checkVersion)
      .catch((error) => console.error("Error fetching version:", error));
  };

  const init = () => {
    const currentVersion = document.getElementById("actual-version").textContent.trim();
    if (!currentVersion) {
      console.log("Dev mode detected, skipping version check.");
      return;
    }
    fetchVersionInfo();
  };

  init();
}

document.addEventListener('DOMContentLoaded', initializeVersionCheck);
