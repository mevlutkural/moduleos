package banner

import "fmt"

func Print(version, env, port, domain string) {
	fmt.Println()
	fmt.Println(`  ███╗   ███╗ ██████╗ ██████╗ ██╗   ██╗██╗     ███████╗ ██████╗ ███████╗`)
	fmt.Println(`  ████╗ ████║██╔═══██╗██╔══██╗██║   ██║██║     ██╔════╝██╔═══██╗██╔════╝`)
	fmt.Println(`  ██╔████╔██║██║   ██║██║  ██║██║   ██║██║     █████╗  ██║   ██║███████╗`)
	fmt.Println(`  ██║╚██╔╝██║██║   ██║██║  ██║██║   ██║██║     ██╔══╝  ██║   ██║╚════██║`)
	fmt.Println(`  ██║ ╚═╝ ██║╚██████╔╝██████╔╝╚██████╔╝███████╗███████╗╚██████╔╝███████║`)
	fmt.Println(`  ╚═╝     ╚═╝ ╚═════╝ ╚═════╝  ╚═════╝ ╚══════╝╚══════╝ ╚═════╝ ╚══════╝`)
	fmt.Println()
	fmt.Printf("  version    %s\n", version)
	fmt.Printf("  env        %s\n", env)
	fmt.Printf("  port       :%s\n", port)
	fmt.Printf("  domain     *.%s\n", domain)
	fmt.Println()
	fmt.Println("  ─────────────────────────────────────────────────────────────────────")
	fmt.Println()
}
