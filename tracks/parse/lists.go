package parse

// The domain and name lists ClassifyAd reads. Ported from
// adhunters-collector e20148c, internal/sweeper/lists.go.

var BigBrands = map[string]struct{}{
	"amazon.com": {}, "temu.com": {}, "walmart.com": {}, "target.com": {}, "homedepot.com": {},
	"ebay.com": {}, "shein.com": {}, "wayfair.com": {}, "bestbuy.com": {}, "nike.com": {},
	"apple.com": {}, "capitalone.com": {}, "ulta.com": {}, "ariat.com": {}, "ups.com": {},
	"costco.com": {}, "lowes.com": {}, "macys.com": {}, "kohl.com": {}, "aliexpress.com": {}, "flipp.com": {},
	"fisherinvestments.com": {}, "intuit.com": {}, "creditkarma.com": {}, "smartasset.com": {},
	"kalshi.com": {}, "robinhood.com": {}, "schwab.com": {}, "fidelity.com": {}, "vanguard.com": {},
	"chase.com": {}, "bankofamerica.com": {}, "wellsfargo.com": {}, "citi.com": {}, "morganstanley.com": {},
	"goldmansachs.com": {}, "americanexpress.com": {}, "discover.com": {}, "johnhancock.com": {},
	"manulife.com": {}, "zacks.com": {},
	"globelifeinsurance.com": {}, "globelife.com": {}, "statefarm.com": {}, "geico.com": {},
	"progressive.com": {}, "allstate.com": {}, "libertymutual.com": {}, "nationwide.com": {},
	"travelers.com": {}, "usaa.com": {}, "prudential.com": {}, "metlife.com": {}, "newyorklife.com": {},
	"northwesternmutual.com": {}, "aetna.com": {}, "cigna.com": {}, "humana.com": {}, "uhc.com": {},
	"goodrx.com": {}, "cvs.com": {}, "walgreens.com": {}, "dexcom.com": {}, "pfizer.com": {},
	"lilly.com": {}, "jnj.com": {}, "abbvie.com": {}, "mayoclinic.org": {},
	"xometry.com": {}, "theupsstore.com": {}, "fedex.com": {}, "ge.com": {}, "celonis.com": {},
	"blinkist.com": {}, "hillsdale.edu": {},
	"ford.com": {}, "chevrolet.com": {}, "toyota.com": {}, "honda.com": {}, "bmw.com": {},
	"mercedes-benz.com": {}, "audi.com": {}, "tesla.com": {}, "hyundai.com": {}, "kia.com": {},
	"nissan.com": {}, "subaru.com": {}, "vw.com": {}, "volkswagen.com": {}, "lexus.com": {},
	"porsche.com": {}, "volvo.com": {}, "jeep.com": {}, "ramtrucks.com": {}, "gmc.com": {}, "cadillac.com": {},
	"plarium.com": {}, "wargaming.net": {}, "fanatics.com": {},
}

var BigBrandNames = []string{
	"fisher investments", "goodrx", "globe life", "intuit", "credit karma",
	"smartasset", "cvs health", "dexcom", "xometry", "the ups store",
	"ups store", "blinkist", "hillsdale college", "manulife", "john hancock",
	"zacks investment", "cio | celonis", "fanatics sportsbook",
	"raid: shadow legends", "world of tanks", "general electric", "ge appliances",
}

var TravelDomains = map[string]struct{}{
	"palladiumhotelgroup.com": {}, "atlantisbahamas.com": {}, "marriott.com": {}, "hilton.com": {},
	"hyatt.com": {}, "wyndham.com": {}, "sandals.com": {}, "riu.com": {}, "fourseasons.com": {},
	"accor.com": {}, "ihg.com": {}, "bestwestern.com": {}, "choicehotels.com": {},
	"booking.com": {}, "expedia.com": {}, "airbnb.com": {}, "vrbo.com": {}, "tripadvisor.com": {},
	"kayak.com": {}, "trivago.com": {}, "priceline.com": {}, "hotels.com": {}, "agoda.com": {},
	"travelocity.com": {}, "orbitz.com": {}, "hotwire.com": {},
	"carnival.com": {}, "royalcaribbean.com": {}, "ncl.com": {}, "norwegiancruiseline.com": {},
	"vikingcruises.com": {}, "celebritycruises.com": {}, "princess.com": {}, "msccruises.com": {},
	"hollandamerica.com": {},
	"delta.com":          {}, "united.com": {}, "aa.com": {}, "southwest.com": {}, "jetblue.com": {},
	"emirates.com": {}, "qatarairways.com": {}, "ryanair.com": {}, "easyjet.com": {},
	"lufthansa.com": {}, "britishairways.com": {}, "airfrance.com": {}, "lacompagnie.com": {},
	"enterprise.com": {}, "hertz.com": {}, "avis.com": {}, "budget.com": {}, "alamo.com": {},
	"nationalcar.com": {}, "sixt.com": {}, "turo.com": {}, "tokyo-skytree.jp": {},
}

var TravelBrandPatterns = []string{
	"palladium", "ocean signature resort", "atlantis paradise", "la compagnie",
	"tokyo skytree", "travel + leisure", "cruise", "hotel group", "hotels & resorts",
	"vacation club", "resort & spa", "airline", "airways",
}

var RealEstateDomains = map[string]struct{}{
	"homes.com": {}, "zillow.com": {}, "redfin.com": {}, "realtor.com": {}, "trulia.com": {},
	"apartments.com": {}, "rent.com": {}, "loopnet.com": {}, "compass.com": {},
	"coldwellbanker.com": {}, "remax.com": {}, "century21.com": {}, "sothebysrealty.com": {},
}

var RealEstateBrandPatterns = []string{
	"homes.com", "zillow", "redfin", "realtor.com", "trulia", "apartments.com",
	"loopnet", "century 21", "coldwell banker", "re/max", "sotheby",
}

var ArbitrageDomains = map[string]struct{}{
	"search.yahoo.com": {}, "rhs.search.yahoo.com": {}, "yahoo.com": {}, "system1.com": {},
	"tonic.com": {}, "sedo.com": {}, "ask.com": {}, "search.ch": {}, "bing.com": {},
	"looksmart.com": {}, "infospace.com": {}, "dogpile.com": {},
	"similarsearch.net": {}, "gimica.com": {}, "elitesearches.net": {}, "prosearches.net": {},
	"searcharena.net": {}, "smartsearches.net": {}, "searchlogik.com": {}, "advisorhq.net": {},
	"answersguide.net": {}, "exploreanswers.net": {}, "answerspros.com": {}, "healthsearch123.com": {},
	"gosearchable.com": {}, "searchconnect.com": {}, "searchableonline.com": {}, "trendinganswers.com": {},
}

var ArbitrageBrandKeywords = []string{
	"search ads", "searchresult", "prosearches", "trendingsearch",
	"getsearching", "yahoo search", "search offers", "search tools",
	"similarsearch", "advisorhq", "searcharena", "searchlogik",
	"smartsearches", "freshsearches", "elitesearches", "trendinganswers",
	"gosearchable", "exploreanswers",
}

var NewsDomains = map[string]struct{}{
	"nbcnews.com": {}, "today.com": {}, "eonline.com": {}, "cbsnews.com": {}, "foxnews.com": {},
	"cnn.com": {}, "thehill.com": {}, "dailymail.co.uk": {}, "usatoday.com": {}, "msn.com": {},
	"nytimes.com": {}, "washingtonpost.com": {}, "apnews.com": {}, "reuters.com": {},
	"blavity.com": {}, "afrotech.com": {}, "forbes.com": {}, "caranddriver.com": {},
	"cosmopolitan.com": {}, "countryliving.com": {}, "byrdie.com": {}, "goodhousekeeping.com": {},
	"parents.com": {}, "popularmechanics.com": {}, "runnersworld.com": {}, "townandcountrymag.com": {},
	"wmagazine.com": {}, "womenshealthmag.com": {}, "travelnoire.com": {}, "travelandleisure.com": {},
	"businessinsider.com": {}, "insider.com": {}, "mic.com": {}, "thechannelcompany.com": {},
	"bloomberg.com": {}, "wsj.com": {}, "time.com": {}, "fortune.com": {}, "barrons.com": {},
	"theatlantic.com": {}, "newyorker.com": {}, "wired.com": {}, "techcrunch.com": {}, "theverge.com": {},
}

var NewsBrandKeywords = []string{
	"nbc", "today show", "e! online", "cnn", "fox news", "blavity",
	"afrotech", "travel noire", "forbes", "car & driver", "cosmopolitan",
	"country living", "byrdie", "good housekeeping", "parents",
	"popular mechanics", "runner's world", "town & country", "w magazine",
	"women's health", "travel + leisure", "the channel company",
	"insider", "mic",
}

var PlaceholderHeadlines = map[string]struct{}{
	"for further reading:":                         {},
	"for further reading":                          {},
	"click here for more information":              {},
	"click here for more details":                  {},
	"click here to learn more":                     {},
	"want to know more? click here":                {},
	"want to know more click here":                 {},
	"would you like to know more?":                 {},
	"would you like to know more":                  {},
	"this might be relevant for you":               {},
	"this may be of interest to you!":              {},
	"this may be of interest to you":               {},
	"recommended reading for you":                  {},
	"additional resources on this topic":           {},
	"further insights on this subject":             {},
	"brought to you by":                            {},
	"you might be interested":                      {},
	"you might be interested in the content above": {},
	"explore related insights":                     {},
	"discover more context below":                  {},
	"expand your knowledge here":                   {},
	"default banner title":                         {},
	"default title":                                {},
	"more information available here":              {},
	"for more information:":                        {},
	"for more information":                         {},
	"sponsored recommendation":                     {},
}

var InternalAssetDomains = map[string]struct{}{
	"cdn.taboola.com":             {},
	"trc.taboola.com":             {},
	"images.mediago.io":           {},
	"cdn.stackadapt.com":          {},
	"platform.adtheorent.com":     {},
	"f.wishabi.net":               {},
	"static-content-1.smadex.com": {},
}
