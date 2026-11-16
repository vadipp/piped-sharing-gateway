const fs = require("node:fs");
fs.mkdirSync("web/vendor", {recursive:true});
for (const name of ["shaka-player.ui.js", "controls.css"]) {
    fs.copyFileSync(`node_modules/shaka-player/dist/${name}`, `web/vendor/${name}`);
}
fs.copyFileSync("node_modules/shaka-player/LICENSE", "web/vendor/LICENSE");
