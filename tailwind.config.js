/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./internal/ui/**/*.{gohtml,go}"],
  theme: {
    extend: {
      colors: {
        gordonmascot: {
          lblue: "#007BC0",
          mblue: "#005293",
          hblue: '#011E5C',
          lbeige: "#FFDCB3",
          mbeige: '#D7B49F',
        },
      },
    },
  },
  plugins: [],
}