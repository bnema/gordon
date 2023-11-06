function initializeVersionCheck() {
  const updateStrTemplate =
    "New version %VERSION% available, consider pulling the latest image";

  const checkVersion = (versionData) => {
    const currentVersion = document.getElementById("actual-version").textContent.trim();
    const fetchedVersionNumber = versionData?.amd64?.name.match(/\d+\.\d+\.\d+/)?.[0];

    if (currentVersion !== fetchedVersionNumber) {
      const updateStr = updateStrTemplate.replace("%VERSION%", fetchedVersionNumber);
      console.log(updateStr);
      const updateElement = document.getElementById("update-available");
      updateElement.title = updateStr;
      updateElement.removeAttribute("hidden");
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
    
    // If currentVersion is empty, assume dev mode and do not fetch version info
    if (!currentVersion) {
      console.log("Dev mode detected, skipping version check.");
      return;
    }

    fetchVersionInfo();
  };

  init();
}

// Ensure the document is fully loaded before running the script
document.addEventListener('DOMContentLoaded', initializeVersionCheck);// switched to hyperscript for the most part