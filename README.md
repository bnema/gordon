<h1 align="center">
  <br>
 <img src="https://github.com/bnema/gordon/blob/main/internal/webui/public/assets/imgs/gordon-mascot-mq-trsp.png?raw=true" alt="Gordon" width="192">
  <br>
  Gordon
  <br>
</h1>

<h4 align="center">Minimalist self-hosted containerized app deployment tool.</h4>


Gordon is a tool written in Go that aims to simplify the deployment of your containerized web applications. It automates the process of integrating your locally built image into your self-hosted setup without the need of a container registry. 
Optionally, for the deploy command, Gordon can coordinate domain routing through Traefik (which needs to be pre-installed). When used, this feature eliminates the need for manual configuration and streamlines the deployment process.

## **Key Features**

- **Ease of use**:
  - Push your container images effortlessly using the **`gordon push`** command. A container creation URL is generated upon successful push.

  ![Demo Push](assets/vhs/demo_push.gif?raw=true)

  - Deploy your web applications quickly with **`gordon deploy`**.

  ![Demo Deploy](assets/vhs/demo_deploy.gif?raw=true)

- **Self-Hosted**: Operates within your existing self-hosted environment without interfering with your setup.
- **Self-Updated**: The client can update itself to the latest version with `gordon update`. The backend is updated by pulling the latest image from ghcr.io.
- **Minimal WebUI**: Provides a simple web UI to manage your deployments, domains, exposed ports. Using a very minimal stack (sqlite3, Go templates, HTMX and Tailwind CSS).
- **Simple authentication**: Use GitHub OAuth for the web UI and OAuth 2.0 device authorization grant (device flow) for the CLI client.
- **Automated Routing**: Collaborates with Traefik to automatically route your application to your desired domain or subdomain.
- **One binary**: Gordon follows the Golang philosophy of keeping things simple, offering a single binary for all functionalities.

## **Why ?**

I needed a simple tool that would automate the process of quickly spin up my web apps and preferably not on someone else's computer. And since I wanted to learn Go, I decided to build it myself.

## **Installation / Getting Started / Usage**

For detailed instructions, please refer to the [wiki](https://github.com/bnema/gordon/wiki/)

- [Setting up Gordon’s Backend](https://github.com/bnema/gordon/wiki/Setting-up-Gordon%E2%80%99s-Backend)
- [Setting up Gordon's Client](https://github.com/bnema/gordon/wiki/Setting-up-Gordon's-Client)
- [Deploying your first app](https://github.com/bnema/gordon/wiki/First-deployment)


## **Development Status & Disclaimer**

🛠️ **Note**: Gordon is under heavy development and is absolutely not ready for production. Thank you in advance for taking the time to test it and report any issues you may encounter.

I also want to emphasize that I am far from being a Go expert yet. Hence, if you notice any poor practices, I welcome your feedback—respectfully, of course. It's a valuable part of the learning process!

## **Roadmap beyond 0.1**

- [x] Improved error handling
- [x] Improved logging
- [x] Refined CLI to backend authentication process (utilizing GitHub OAuth Device flow)
- [x] `push` command
- [x] `version` command
- [x] `update` command
- [x] `deploy` command
- [ ] Survey(s) on deploy command (delete if already exists, etc.)
- [ ] Improved web UI
- [ ] Templates for databases (mysql, postgresql, redis, etc.)

Have suggestions? Feel free to open an issue!

## **License**

Gordon is licensed under the GPL-3.0 license. Please see the LICENSE file for more details.
