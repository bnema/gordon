/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    "./internal/webui/**/*.{gohtml,go}",
    "./internal/templating/models/html/**/*.{gohtml, go}",
  ],
  theme: {
    extend: {
      colors: {
        gordonmascot: {
          lblue: "#007BC0",
          mblue: "#005293",
          hblue: "#011E5C",
          lbeige: "#FFDCB3",
          mbeige: "#D7B49F",
        },
        // Custom color scheme
        indigo: {
          50: "#eef2ff",
          100: "#e0e7ff",
          200: "#c7d2fe",
          300: "#a5b4fc",
          400: "#818cf8",
          500: "#6366f1",
          600: "#4f46e5",
          700: "#4338ca",
          800: "#3730a3",
          900: "#312e81",
          950: "#1e1b4b",
        },
        red: {
          50: "#fef2f2",
          100: "#fee2e2",
          200: "#fecaca",
          300: "#fca5a5",
          400: "#f87171",
          500: "#ef4444",
          600: "#dc2626",
          700: "#b91c1c",
          800: "#991b1b",
          900: {
            DEFAULT: "#7f1d1d",
            20: "rgba(127, 29, 29, 0.2)",
          },
          950: "#450a0a",
        },
      },
    },
  },
  plugins: [require("daisyui")],
  daisyui: {
    themes: ["light", "dark", "gordon", "gordon-dark"],
    darkTheme: "gordon-dark",
    base: true,
    styled: true,
    utils: true,
    logs: true,
  },
};
