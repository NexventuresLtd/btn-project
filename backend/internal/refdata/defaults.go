package refdata

// Default is one shipped reference-data row. Seeding is idempotent (ON CONFLICT (kind, code) DO NOTHING),
// so admin edits are never overwritten.
type Default struct {
	Code           string
	EN, AM, RW, FR string
	Meta           map[string]any
}

// Defaults per kind, in display order (sort_order = 10, 20, 30, …).
// Sources: BTN daily production / stock / petty-cash sheets and the SRS (ADM-01).
var Defaults = map[string][]Default{
	KindDefectReason: {
		{"BROKEN_MACHINE", "Broken at the machine", "በማሽኑ ላይ የተሰበረ", "Yamenekeye ku mashini", "Cassé à la machine", nil},
		{"BROKEN_CURING", "Broken during curing", "በማድረቅ ጊዜ የተሰበረ", "Yamenekeye mu gihe cyo kumisha", "Cassé pendant la cure", nil},
		{"HANDLING", "Handling damage", "በአያያዝ የተጎዳ", "Yangiritse mu kuyaterura", "Dommage de manutention", nil},
		{"CRACKED", "Cracked", "የተሰነጠቀ", "Yasadutse", "Fissuré", nil},
		{"SIZE_TOLERANCE", "Size out of tolerance", "መጠኑ ከተፈቀደው ውጭ", "Ingano itujuje ibisabwa", "Dimensions hors tolérance", nil},
		{"OTHER", "Other", "ሌላ", "Ibindi", "Autre", nil},
	},
	KindExceptionFlag: {
		{"MACHINE_BREAKDOWN", "Machine breakdown", "የማሽን ብልሽት", "Imashini yapfuye", "Panne de machine", nil},
		{"WEATHER", "Weather", "የአየር ሁኔታ", "Ikirere", "Météo", nil},
		{"MATERIAL_SHORTAGE", "Shortage of material", "የዕቃ እጥረት", "Ibura ry'ibikoresho", "Manque de matériaux", nil},
		{"POWER_CUT", "Power cut", "የመብራት መቋረጥ", "Umuriro wabuze", "Coupure de courant", nil},
		{"OTHER", "Other", "ሌላ", "Ibindi", "Autre", nil},
	},
	KindUnit: {
		{"PCS", "Pieces", "ቁራጭ", "Ibice", "Pièces", map[string]any{"symbol": "pcs", "decimals": 0}},
		{"BAG", "Bag", "ከረጢት", "Umufuka", "Sac", map[string]any{"symbol": "bag", "decimals": 0}},
		{"KG", "Kilogram", "ኪሎግራም", "Ikilo", "Kilogramme", map[string]any{"symbol": "kg", "decimals": 2}},
		{"LITER", "Litre", "ሊትር", "Litiro", "Litre", map[string]any{"symbol": "L", "decimals": 2}},
		{"TRUCK", "Truck load", "የመኪና ጭነት", "Ikamyo", "Camion", map[string]any{"symbol": "truck", "decimals": 1}},
	},
	KindProductCategory: {
		{"HOLLOW_BLOCK", "Hollow blocks", "ባዶ ብሎኬቶች", "Amatafari y'imyobo", "Blocs creux", nil},
		{"SOLID_BLOCK", "Solid blocks", "ሙሉ ብሎኬቶች", "Amatafari akomeye", "Blocs pleins", nil},
		{"HOURDI", "Hourdi beam blocks", "የሆርዲ ብሎኬቶች", "Amatafari ya hourdi", "Hourdis", nil},
		{"PAVER", "Pavers", "የወለል ንጣፎች", "Pave", "Pavés", nil},
		{"OTHER", "Other", "ሌላ", "Ibindi", "Autre", nil},
	},
	KindMaterialCategory: {
		{"CEMENT", "Cement", "ሲሚንቶ", "Sima", "Ciment", nil},
		{"AGGREGATE", "Aggregates (sand, gravel, dust)", "ጠጠርና አሸዋ", "Umucanga n'amabuye", "Granulats (sable, gravier, poussière)", nil},
		{"ADDITIVE", "Additives", "ተጨማሪዎች", "Inyongeramusaruro", "Adjuvants", nil},
		{"FUEL", "Fuel & lubricants", "ነዳጅና ቅባት", "Lisansi n'amavuta", "Carburant et lubrifiants", nil},
		{"SPARE_PART", "Spare parts", "መለዋወጫ", "Ibyuma by'insimburabuguzi", "Pièces de rechange", nil},
		{"OTHER", "Other", "ሌላ", "Ibindi", "Autre", nil},
	},
	// Migrated from the fixed PettyCashCategory enum (FLOAT_DEPOSIT is a float movement, not an expense).
	KindExpenseCategory: {
		{"WAGES", "Wages", "የቀን ሠራተኛ ደመወዝ", "Imishahara y'abakozi b'ibihe", "Salaires journaliers", map[string]any{"pettyCash": "WAGES"}},
		{"SALARY", "Salary", "ደመወዝ", "Umushahara", "Salaires", map[string]any{"pettyCash": "SALARY"}},
		{"TRANSPORT", "Transport", "ትራንስፖርት", "Ingendo", "Transport", map[string]any{"pettyCash": "TRANSPORT"}},
		{"FUEL", "Fuel", "ነዳጅ", "Lisansi", "Carburant", map[string]any{"pettyCash": "FUEL"}},
		{"INTERNET_COMMS", "Internet & comms", "ኢንተርኔት እና ግንኙነት", "Internet n'itumanaho", "Internet et télécoms", map[string]any{"pettyCash": "INTERNET_COMMS"}},
		{"MEALS", "Meals", "ምግብ", "Amafunguro", "Repas", map[string]any{"pettyCash": "MEALS"}},
		{"NIGHT_SHIFT", "Night-shift allowance", "የሌሊት ፈረቃ አበል", "Indamu y'akazi k'ijoro", "Prime de nuit", map[string]any{"pettyCash": "NIGHT_SHIFT"}},
		{"MATERIALS", "Materials", "ዕቃዎች", "Ibikoresho", "Matériaux", map[string]any{"pettyCash": "MATERIALS"}},
		{"MAINTENANCE", "Maintenance", "ጥገና", "Gusana", "Entretien", map[string]any{"pettyCash": "MAINTENANCE"}},
		{"OFFICE", "Office", "ቢሮ", "Ibiro", "Bureau", map[string]any{"pettyCash": "OFFICE"}},
		{"OTHER", "Other", "ሌላ", "Ibindi", "Autre", map[string]any{"pettyCash": "OTHER"}},
	},
	KindDepartment: {
		{"PRODUCTION", "Production", "ምርት", "Umusaruro", "Production", nil},
		{"SALES", "Sales", "ሽያጭ", "Ubucuruzi", "Ventes", nil},
		{"FINANCE", "Finance", "ፋይናንስ", "Imari", "Finances", nil},
		{"ADMIN", "Administration", "አስተዳደር", "Ubutegetsi", "Administration", nil},
		{"PROCUREMENT", "Procurement", "ግዥ", "Amasoko", "Achats", nil},
		{"HQ", "Head office", "ዋና መሥሪያ ቤት", "Icyicaro gikuru", "Siège", nil},
	},
	KindClientType: {
		{"COMPANY", "Company", "ድርጅት", "Ikigo", "Entreprise", nil},
		{"INDIVIDUAL", "Individual", "ግለሰብ", "Umuntu ku giti cye", "Particulier", nil},
		{"GOVERNMENT", "Government", "መንግሥት", "Leta", "Administration publique", nil},
		{"CONTRACTOR", "Contractor", "ተቋራጭ", "Rwiyemezamirimo", "Entrepreneur", nil},
	},
	KindPaymentTerm: {
		{"CASH", "Cash on order", "ጥሬ ገንዘብ በትዕዛዝ ጊዜ", "Kwishyura ako kanya", "Comptant à la commande", map[string]any{"days": 0}},
		{"NET7", "7 days", "7 ቀናት", "Iminsi 7", "7 jours", map[string]any{"days": 7}},
		{"NET14", "14 days", "14 ቀናት", "Iminsi 14", "14 jours", map[string]any{"days": 14}},
		{"NET30", "30 days", "30 ቀናት", "Iminsi 30", "30 jours", map[string]any{"days": 30}},
		{"DEPOSIT_BALANCE", "Deposit + balance on delivery", "ቅድመ ክፍያ + ቀሪው በርክክብ ጊዜ", "Avansi + asigaye ku itangwa", "Acompte + solde à la livraison", map[string]any{"days": 0, "depositPercent": 50}},
	},
	KindDeliveryZone: {
		{"KIGALI", "Kigali", "ኪጋሊ", "Kigali", "Kigali", nil},
		{"OUTSIDE_KIGALI", "Outside Kigali", "ከኪጋሊ ውጭ", "Hanze ya Kigali", "Hors de Kigali", nil},
	},
}
