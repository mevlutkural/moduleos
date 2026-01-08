import { useTranslation } from "react-i18next";
import { Languages } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";

export function LanguageToggle() {
  const { i18n } = useTranslation();

  const toggleLanguage = (lng: string) => {
    i18n.changeLanguage(lng);
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="size-9 rounded-xl transition-all hover:bg-primary/10 hover:text-primary"
        >
          <Languages className="size-5" />
          <span className="sr-only">Toggle language</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        className="w-40 rounded-xl p-2 border-border/50 backdrop-blur-xl bg-card/80"
      >
        <DropdownMenuItem
          onClick={() => toggleLanguage("tr")}
          className={`rounded-lg cursor-pointer font-medium ${
            i18n.language === "tr" ? "bg-primary/10 text-primary" : ""
          }`}
        >
          Türkçe
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() => toggleLanguage("en")}
          className={`rounded-lg cursor-pointer font-medium ${
            i18n.language.startsWith("en") ? "bg-primary/10 text-primary" : ""
          }`}
        >
          English
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
