const fs = require("node:fs");
fs.mkdirSync("web/vendor", {recursive:true});
for (const name of ["shaka-player.ui.js", "controls.css"]) {
    const source = `node_modules/shaka-player/dist/${name}`;
    if (name === "controls.css") {
        // Use system fonts; the upstream stylesheet includes a Google Fonts URL.
        const css = fs.readFileSync(source, "utf8").replace(/@font-face\{[^}]*\}/g, "");
        fs.writeFileSync(`web/vendor/${name}`, css);
    } else {
        fs.copyFileSync(source, `web/vendor/${name}`);
    }
}
fs.copyFileSync("node_modules/shaka-player/LICENSE", "web/vendor/LICENSE");
