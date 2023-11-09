<h1 align="center">
  <br>
 <img src="https://github.com/bnema/gordon/blob/main/internal/webui/public/assets/imgs/gordon-mascot-mq-trsp.png?raw=true" alt="Gordon" width="192">
  <br>
  Gordon
  <br>
</h1>

<h4 align="center">Minimalist self-hosted containerized app deployment tool.</h4>


Gordon is a tool written in Go that aims to simplify the deployment of your containerized web applications. It automates the process of integrating your locally built image into your self-hosted setup. Additionally, Gordon coordinates domain routing through Traefik, which needs to be pre-installed. This eliminates the need for manual configuration and streamlines the development process.

## **Key Features**

- **Ease of use**: Deploy container images effortlessly using the command **`gordon deploy`**.
- **Self-Hosted**: Operates within your existing self-hosted environment and don't interfere with your existing setup.
- **Web UI**: Provides a simple web UI to manage your deployments, domains, exposed ports, and more.
- **Automated Routing**: Collaborates with Traefik to automatically route your application to your desired domain or subdomain.
- **One binary**: Gordon follows the Golang philosophy of keeping things simple, offering a single binary for all functionalities.
- **Minimal stack**: Sqlite3 (no ORM) for the db, the web UI is built using Go templates, HTMX and Tailwind CSS
- **Simple authentication**: Use Github OAuth to authenticate with the web UI and a token for the CLI.

![Demo Deploy](assets/vhs/demo_deploy.gif?raw=true)


## **Why ?**

I found myself spending too much time manually deploying my containerized web apps via remote SSH. I needed a simple tool that would automate the process, allowing me to quickly spin up my web apps and preferably not on someone else's computer. And since I wanted to learn Go, I decided to build it myself.

## **Installation / Getting Started / Usage**

For detailed instructions, please refer to the wiki:  <https://github.com/bnema/gordon/wiki/>


## **Development Status & Disclaimer**

🛠️ **Note**: Gordon is under heavy development and is absolutely not ready for production. Thank you in advance for taking the time to test it and report any issues you may encounter.

I also want to emphasize that I am far from being a Go expert yet. Hence, if you notice any poor practices, I welcome your feedback—respectfully, of course. It's a valuable part of the learning process!

## **Roadmap beyond 0.1**

- Bug fixes (obviously)
- Add tests
- Better error handling and logging
- Sexier WebUI with more features while keeping it simple (spa)
- Sexier CLI (💕 [Charm](https://github.com/charmbracelet))
- Refined CLI to backend authentication process (utilizing GitHub OAuth Device flow)
- `deploy` command with no params
- `push` command
- Templates for databases (mysql, postgresql, redis, etc.)

Have suggestions? Feel free to open an issue!

## **License**

Gordon is licensed under the GPL-3.0 license. Please see the LICENSE file for more details.