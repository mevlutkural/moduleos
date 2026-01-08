import * as React from "react";
import { Tab, TabGroup, TabList, TabPanel, TabPanels } from "@headlessui/react";
import { cn } from "@/shared/lib/utils/cn";

interface TabsProps {
  defaultValue?: string;
  value?: string;
  onValueChange?: (value: string) => void;
  children: React.ReactNode;
  className?: string;
}

const TabsContext = React.createContext<{
  value?: string;
  onValueChange?: (value: string) => void;
  tabIds: string[];
  registerTab: (id: string) => void;
}>({
  tabIds: [],
  registerTab: () => {},
});

export function Tabs({
  defaultValue,
  value,
  onValueChange,
  children,
  className,
}: TabsProps) {
  const [tabIds, setTabIds] = React.useState<string[]>([]);
  const registerTab = React.useCallback((id: string) => {
    setTabIds((prev) => (prev.includes(id) ? prev : [...prev, id]));
  }, []);

  const selectedIndex = React.useMemo(() => {
    const activeValue = value || defaultValue;
    if (!activeValue) return 0;
    const index = tabIds.indexOf(activeValue);
    return index === -1 ? 0 : index;
  }, [value, defaultValue, tabIds]);

  const handleChange = (index: number) => {
    const id = tabIds[index];
    if (id && onValueChange) {
      onValueChange(id);
    }
  };

  return (
    <TabsContext.Provider value={{ value, onValueChange, tabIds, registerTab }}>
      <TabGroup
        selectedIndex={selectedIndex}
        onChange={handleChange}
        className={cn("w-full", className)}
      >
        {children}
      </TabGroup>
    </TabsContext.Provider>
  );
}

export function TabsList({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <TabList
      className={cn(
        "flex items-center gap-1 p-1 bg-accent/30 rounded-2xl w-fit border border-border/50",
        className
      )}
    >
      {children}
    </TabList>
  );
}

interface TabsTriggerProps {
  value: string;
  children: React.ReactNode;
  className?: string;
}

export function TabsTrigger({ value, children, className }: TabsTriggerProps) {
  const { registerTab } = React.useContext(TabsContext);

  React.useEffect(() => {
    registerTab(value);
  }, [value, registerTab]);

  return (
    <Tab
      className={({ selected }) =>
        cn(
          "flex items-center gap-2 px-6 py-2.5 rounded-xl text-sm font-bold transition-all outline-none",
          selected
            ? "bg-card text-primary shadow-sm border border-border/50"
            : "text-muted-foreground hover:text-foreground",
          className
        )
      }
    >
      {children}
    </Tab>
  );
}

export { TabPanels as TabsContentGroup };

export function TabsContent({
  children,
  className,
}: {
  value: string;
  children: React.ReactNode;
  className?: string;
}) {
  // Component order in TabPanels corresponds to Tab order in TabList.
  // We don't need the value for logic here but we keep it for API compatibility (shadcn style).

  return (
    <TabPanel className={cn("outline-none", className)}>{children}</TabPanel>
  );
}

export {
  TabGroup as TabsRoot,
  TabList as TabsListPrimitive,
  Tab as TabsTriggerPrimitive,
  TabPanel as TabsContentPrimitive,
};
