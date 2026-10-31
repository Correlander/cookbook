# Cookbook marketing site

This is a separate, dependency-free static site for the public project overview. It is intentionally independent from the signed-in application dashboard and can be hosted on any static asset service.

## Preview

Open `index.html` directly, or serve this directory with any static HTTP server. No build step is required.

## Deploy

Upload the contents of this directory to a static host. Keep the application deployment separate. Update the project links in `index.html` if the repository URL changes. Managed hosting is not advertised as available until the project has an actual service and support process.

The page includes a semantic heading structure, responsive layout, skip link, reduced-motion support, page title, description, and Open Graph summary. It uses system fonts, local CSS art, and a compressed screenshot of the public Cookbook app home; it makes no external asset requests.
