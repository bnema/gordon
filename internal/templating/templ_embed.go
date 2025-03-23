package templating

// This file contains templ components embeddings
// As we migrate from traditional Go templates to templ,
// we'll add the appropriate code here to handle templ templates.

import (
	// Import templ generated templates, which will make them available to the application
	_ "github.com/bnema/gordon/internal/templating/models/templ/components"
	_ "github.com/bnema/gordon/internal/templating/models/templ/layouts"
	_ "github.com/bnema/gordon/internal/templating/models/templ/pages/admin"
	_ "github.com/bnema/gordon/internal/templating/models/templ/pages/login"
)