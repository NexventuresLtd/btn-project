import { defineNs } from "../define";

/** SRS §5.2: dashboards and reports read from the device cache ("as of"), online-only approvals, admin and downloads. */
export default defineNs({
  en: {
    "offlineview.banner.offline": "Offline — data as of {at}",
    "offlineview.banner.unreachable": "Server not reachable — data as of {at}",
    "offlineview.banner.text": "Shown from the copy saved on this device. It refreshes automatically when the connection returns.",
    "offlineview.banner.otherFilters": "Saved for other filters: these are the last figures opened on this device.",
    "offlineview.online.action": "Needs a connection: approvals, verifications and administration are done online. Reconnect and try again.",
    "offlineview.online.download": "Downloads need a connection: PDF, Excel and CSV files are made by the server. The figures on screen come from this device.",
    "offlineview.zone.title": "This page needs a connection",
    "offlineview.zone.text": "Administration, user management and email sending are not available offline. Nothing is queued on this device — reconnect to continue.",
  },
  am: {
    "offlineview.banner.offline": "ከመስመር ውጭ — መረጃው እስከ {at} ድረስ",
    "offlineview.banner.unreachable": "አገልጋዩ አይገኝም — መረጃው እስከ {at} ድረስ",
    "offlineview.banner.text": "በዚህ መሣሪያ ላይ ከተቀመጠው ቅጂ የሚታይ ነው። ግንኙነቱ ሲመለስ በራሱ ይታደሳል።",
    "offlineview.banner.otherFilters": "ለሌሎች ማጣሪያዎች የተቀመጠ፦ እነዚህ በዚህ መሣሪያ ላይ የተከፈቱ የመጨረሻ አሃዞች ናቸው።",
    "offlineview.online.action": "ግንኙነት ያስፈልጋል፦ ማጽደቅ፣ ማረጋገጥ እና አስተዳደር በመስመር ላይ ይከናወናሉ። እንደገና ተገናኝተው ይሞክሩ።",
    "offlineview.online.download": "ማውረድ ግንኙነት ይፈልጋል፦ PDF፣ Excel እና CSV ፋይሎች በአገልጋዩ ይዘጋጃሉ። በማያ ገጹ ላይ ያሉት አሃዞች ከዚህ መሣሪያ ናቸው።",
    "offlineview.zone.title": "ይህ ገጽ ግንኙነት ይፈልጋል",
    "offlineview.zone.text": "አስተዳደር፣ የተጠቃሚ አስተዳደር እና ኢሜይል መላክ ከመስመር ውጭ አይገኙም። በዚህ መሣሪያ ላይ ምንም አይሰለፍም — ለመቀጠል እንደገና ይገናኙ።",
  },
  rw: {
    "offlineview.banner.offline": "Nta murandasi — amakuru yo ku wa {at}",
    "offlineview.banner.unreachable": "Seriveri ntigerwaho — amakuru yo ku wa {at}",
    "offlineview.banner.text": "Byerekanwa bivuye kuri kopi ibitswe kuri iki gikoresho. Bizivugurura ubwabyo umurandasi nugaruka.",
    "offlineview.banner.otherFilters": "Byabitswe ku yandi mayunguruzo: aya ni yo mibare ya nyuma yafunguwe kuri iki gikoresho.",
    "offlineview.online.action": "Bisaba umurandasi: kwemeza, kugenzura n'ubuyobozi bikorwa umurandasi uhari. Ongera wihuze ugerageze.",
    "offlineview.online.download": "Gukuramo bisaba umurandasi: amadosiye ya PDF, Excel na CSV akorwa na seriveri. Imibare iri kuri ecran ivuye kuri iki gikoresho.",
    "offlineview.zone.title": "Iyi paji isaba umurandasi",
    "offlineview.zone.text": "Ubuyobozi, gucunga abakoresha no kohereza imeyili ntibikora nta murandasi. Nta kintu gishyirwa ku murongo kuri iki gikoresho — ongera wihuze ukomeze.",
  },
  fr: {
    "offlineview.banner.offline": "Hors ligne — données au {at}",
    "offlineview.banner.unreachable": "Serveur injoignable — données au {at}",
    "offlineview.banner.text": "Affiché depuis la copie enregistrée sur cet appareil. Mise à jour automatique au retour de la connexion.",
    "offlineview.banner.otherFilters": "Enregistré pour d'autres filtres : ce sont les derniers chiffres ouverts sur cet appareil.",
    "offlineview.online.action": "Connexion requise : les approbations, vérifications et l'administration se font en ligne. Reconnectez-vous et réessayez.",
    "offlineview.online.download": "Les téléchargements exigent une connexion : les fichiers PDF, Excel et CSV sont produits par le serveur. Les chiffres à l'écran proviennent de cet appareil.",
    "offlineview.zone.title": "Cette page exige une connexion",
    "offlineview.zone.text": "L'administration, la gestion des utilisateurs et l'envoi d'e-mails ne sont pas disponibles hors ligne. Rien n'est mis en file sur cet appareil — reconnectez-vous pour continuer.",
  },
});
