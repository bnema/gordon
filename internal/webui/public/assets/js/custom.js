import 'dotenv/config'; // Import dotenv to load environment variables


function initializeVersionCheck() {
  // Dummy data for the remote version (as if fetched from a server)
  const updateStrTemplate = "New version %VERSION% available, consider pulling the latest image";

  const checkVersion = (versionData, currentVersion) => {
    const fetchedVersionNumber = versionData?.amd64?.name.match(/\d+\.\d+\.\d+/)?.[0];

    if (currentVersion !== fetchedVersionNumber) {
      const updateStr = updateStrTemplate.replace("%VERSION%", fetchedVersionNumber);
      console.log(updateStr);
      const updateElement = document.getElementById("update-available");
      updateElement.title = updateStr;
      updateElement.toggleAttribute("hidden", false);
      updateElement.classList.remove("hidden"); // Remove the Tailwind `hidden` class
      updateElement.classList.add("block"); // Add the Tailwind `block` class
    } else {
      console.log("No new version. Current version is up-to-date:", currentVersion);
    }
  };

  const fetchVersionInfo = (currentVersion) => {
    // os.Get env proxy url + /version
    fetch(`${process.env.PROXY_URL}/version`)
      .then((response) => response.json())
      .then((versionData) => checkVersion(versionData, currentVersion))
      .catch((error) => console.error("Error fetching version:", error));
  };

  const init = () => {
    const currentVersionElement = document.getElementById("actual-version");
    if (!currentVersionElement) {
      console.log("Error: #actual-version element not found.");
      return;
    }
    const currentVersion = currentVersionElement.textContent.trim();
    if (!currentVersion) {
      console.log("Dev mode detected, skipping version check.");
      return;
    }
    fetchVersionInfo(currentVersion);
  };

  init();
}

document.addEventListener('DOMContentLoaded', initializeVersionCheck);
