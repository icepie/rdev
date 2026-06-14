(function () {
  function preferredLang() {
    const saved = localStorage.getItem('rdevLang');
    if (saved === 'zh' || saved === 'en') return saved;
    return (navigator.language || 'en').toLowerCase().startsWith('zh') ? 'zh' : 'en';
  }

  function effectiveTheme(choice) {
    if (choice === 'light' || choice === 'dark') return choice;
    return matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }

  const themeChoice = localStorage.getItem('rdevTheme') || 'auto';
  const theme = effectiveTheme(themeChoice);
  const lang = preferredLang();

  document.documentElement.dataset.theme = theme;
  document.documentElement.dataset.themeChoice = themeChoice;
  document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
})();
