<h1 align="center">
  <br>
 <img src="https://github.com/bnema/gordon/blob/main/internal/webui/public/assets/imgs/gordon-mascot-mq-trsp.png?raw=true" alt="Gordon" width="192">
  <br>
  Gordon
  <br>
</h1>

<h4 align="center">Minimalist self-hosted containerized app deployment tool.</h4>


Gordon is a tool based on Golang that aims to simplify the deployment of your containerized applications. It automates the process of integrating your locally built image into your self-hosted setup. Additionally, Gordon coordinates domain routing through Traefik, which needs to be pre-installed. This eliminates the need for manual configuration and streamlines the development process.

## **Key Features**

- **Quick Deployment**: Deploy container images effortlessly using the command **`gordon ipush <image:version>`**
- **Self-Hosted**: Operates within your existing self-hosted environment.
- **Web UI**: Provides a simple web UI to manage your deployments, domains, exposed ports, and more.
- **Automated Routing**: Collaborates with Traefik to automatically route your application to your desired domain or subdomain.
- **One binary**: Gordon follows the Golang philosophy of keeping things simple, offering a single binary for all functionalities.
- **Minimal stack**: Sqlite3 is used as in memory database (no ORM), the web UI is built using Go templates, HTMX and Tailwind CSS

## **Why ?**

I found myself spending excessive time manually deploying my containerized applications using remote SSH. Being a lazy developer, I wanted a tool that would automate this process. However, I couldn't find one that met my requirements (dead simple), so I decided to make my own.

## **Development Status**

🛠️ **Note**: This project is currently under heavy development. A 0.1 release with basic features will be available soon.

## **License**

Gordon is licensed under the GPL-3.0 license. Please see the LICENSE file for more details.
