import { defineNs } from "../define";

/** AUTH-05: account lockout policy, set by the Super Administrator in Settings. */
export default defineNs({
  en: {
    "lockout.title": "Account lockout",
    "lockout.description": "Protects accounts against password guessing. Only the Super Administrator can change it.",
    "lockout.failuresLabel": "Failed sign-ins before lockout",
    "lockout.failuresHint": "After {n} wrong passwords in a row the account is locked.",
    "lockout.minutesLabel": "Lockout period (minutes)",
    "lockout.minutesHint": "The account unlocks by itself after {n} minutes; an administrator can unlock it sooner from Users.",
    "lockout.readOnly": "Only the Super Administrator can change the lockout policy.",
    "lockout.err.failures": "Between {min} and {max}",
    "lockout.err.minutes": "Between {min} and {max} minutes",
  },
  am: {
    "lockout.title": "የመለያ መቆለፊያ",
    "lockout.description": "መለያዎችን ከይለፍ ቃል ግምት ይጠብቃል። መቀየር የሚችለው ዋና አስተዳዳሪው ብቻ ነው።",
    "lockout.failuresLabel": "ከመቆለፉ በፊት ያልተሳኩ የመግቢያ ሙከራዎች",
    "lockout.failuresHint": "{n} የተሳሳቱ የይለፍ ቃሎች በተከታታይ ከገቡ መለያው ይቆለፋል።",
    "lockout.minutesLabel": "የመቆለፊያ ጊዜ (ደቂቃ)",
    "lockout.minutesHint": "መለያው ከ{n} ደቂቃ በኋላ በራሱ ይከፈታል፤ አስተዳዳሪ ከተጠቃሚዎች ገጽ ቀደም ብሎ ሊከፍተው ይችላል።",
    "lockout.readOnly": "የመቆለፊያ ፖሊሲውን መቀየር የሚችለው ዋና አስተዳዳሪው ብቻ ነው።",
    "lockout.err.failures": "ከ{min} እስከ {max}",
    "lockout.err.minutes": "ከ{min} እስከ {max} ደቂቃ",
  },
  rw: {
    "lockout.title": "Gufunga konti",
    "lockout.description": "Birinda konti abagerageza gukeka ijambobanga. Umuyobozi mukuru wa sisitemu wenyine ni we ushobora kubihindura.",
    "lockout.failuresLabel": "Inshuro zo kwinjira zinanirwa mbere yo gufunga",
    "lockout.failuresHint": "Nyuma y'amagambobanga {n} atari yo akurikiranye, konti irafungwa.",
    "lockout.minutesLabel": "Igihe konti imara ifunze (iminota)",
    "lockout.minutesHint": "Konti yifungura ubwayo nyuma y'iminota {n}; umuyobozi ashobora kuyifungura mbere mu Bakoresha.",
    "lockout.readOnly": "Umuyobozi mukuru wa sisitemu wenyine ni we ushobora guhindura amabwiriza yo gufunga konti.",
    "lockout.err.failures": "Hagati ya {min} na {max}",
    "lockout.err.minutes": "Hagati y'iminota {min} na {max}",
  },
  fr: {
    "lockout.title": "Verrouillage des comptes",
    "lockout.description": "Protège les comptes contre les tentatives de deviner le mot de passe. Seul le super-administrateur peut le modifier.",
    "lockout.failuresLabel": "Échecs de connexion avant verrouillage",
    "lockout.failuresHint": "Après {n} mots de passe erronés d'affilée, le compte est verrouillé.",
    "lockout.minutesLabel": "Durée du verrouillage (minutes)",
    "lockout.minutesHint": "Le compte se déverrouille seul après {n} minutes ; un administrateur peut le déverrouiller plus tôt depuis Utilisateurs.",
    "lockout.readOnly": "Seul le super-administrateur peut modifier la politique de verrouillage.",
    "lockout.err.failures": "Entre {min} et {max}",
    "lockout.err.minutes": "Entre {min} et {max} minutes",
  },
});
